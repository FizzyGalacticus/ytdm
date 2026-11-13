package storage

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
)

// Dialect identifies which SQL backend a Storage talks to.
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// dbConn wraps *sql.DB so the package's queries can be written once, in SQLite's `?`
// placeholder style, and still run on PostgreSQL. For Postgres it rewrites placeholders
// to `$1, $2, ...` and binds Go bools as 0/1, since the shared schema stores flags as
// INTEGER on both backends (database/sql already scans 0/1 back into a bool).
//
// Concurrency: SQLite runs on a single connection, so every statement and transaction
// is serialized. On Postgres, reads run in parallel across the connection pool (the
// main performance win: UI polling, feed checks, and download workers no longer queue
// behind one another), while writes still go through writeMu one at a time. Several
// storage transactions read a row and then write based on what they saw (e.g. "insert
// the video unless it exists"); serializing writes keeps exactly the semantics they
// were written against on SQLite, instead of racing into unique-key errors. ytdm runs
// as a single process, so an in-process lock is sufficient.
type dbConn struct {
	db      *sql.DB
	dialect Dialect
	writeMu *sync.Mutex // nil for SQLite, whose single connection already serializes
}

// dbTx is the transaction counterpart of dbConn. On Postgres it holds the connection's
// write lock from Begin until Commit or Rollback.
type dbTx struct {
	tx      *sql.Tx
	dialect Dialect
	release func()
}

func newDBConn(db *sql.DB, dialect Dialect) *dbConn {
	c := &dbConn{db: db, dialect: dialect}
	if dialect == DialectPostgres {
		c.writeMu = &sync.Mutex{}
	}
	return c
}

func (c *dbConn) lockWrites() func() {
	if c.writeMu == nil {
		return func() {}
	}
	c.writeMu.Lock()
	var once sync.Once
	return func() { once.Do(c.writeMu.Unlock) }
}

// Exec runs a statement outside a transaction. Every Exec in this package is a write.
func (c *dbConn) Exec(query string, args ...any) (sql.Result, error) {
	unlock := c.lockWrites()
	defer unlock()
	return c.db.Exec(rebind(c.dialect, query), bindArgs(c.dialect, args)...)
}

func (c *dbConn) Query(query string, args ...any) (*sql.Rows, error) {
	return c.db.Query(rebind(c.dialect, query), bindArgs(c.dialect, args)...)
}

func (c *dbConn) QueryRow(query string, args ...any) *sql.Row {
	return c.db.QueryRow(rebind(c.dialect, query), bindArgs(c.dialect, args)...)
}

// Begin starts a transaction. Transactions are used for writes, so on Postgres this
// waits for the write lock and holds it until Commit or Rollback.
func (c *dbConn) Begin() (*dbTx, error) {
	unlock := c.lockWrites()
	tx, err := c.db.Begin()
	if err != nil {
		unlock()
		return nil, err
	}
	return &dbTx{tx: tx, dialect: c.dialect, release: unlock}, nil
}

func (c *dbConn) Close() error {
	return c.db.Close()
}

func (t *dbTx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.Exec(rebind(t.dialect, query), bindArgs(t.dialect, args)...)
}

func (t *dbTx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.tx.Query(rebind(t.dialect, query), bindArgs(t.dialect, args)...)
}

func (t *dbTx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRow(rebind(t.dialect, query), bindArgs(t.dialect, args)...)
}

// Prepare returns a statement whose placeholders are already rebound. Arguments passed
// to the returned *sql.Stmt are not converted, so only use it with non-bool arguments.
func (t *dbTx) Prepare(query string) (*sql.Stmt, error) {
	return t.tx.Prepare(rebind(t.dialect, query))
}

func (t *dbTx) Commit() error {
	defer t.release()
	return t.tx.Commit()
}

// Rollback is safe to call after Commit (the usual `defer tx.Rollback()`); the write
// lock is released only once.
func (t *dbTx) Rollback() error {
	defer t.release()
	return t.tx.Rollback()
}

// rebind converts `?` placeholders to PostgreSQL's numbered `$n` form, leaving any `?`
// inside single-quoted string literals (e.g. 'https://www.youtube.com/watch?v=') alone.
func rebind(d Dialect, query string) string {
	if d != DialectPostgres || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	inQuote := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case ch == '\'':
			inQuote = !inQuote // a doubled '' toggles twice, which is still correct
			b.WriteByte(ch)
		case ch == '?' && !inQuote:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func bindArgs(d Dialect, args []any) []any {
	if d != DialectPostgres {
		return args
	}
	for i, a := range args {
		if v, ok := a.(bool); ok {
			if v {
				args[i] = int64(1)
			} else {
				args[i] = int64(0)
			}
		}
	}
	return args
}
