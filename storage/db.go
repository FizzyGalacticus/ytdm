package storage

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
// Concurrency: writes (Exec and transactions) go through writeMu one at a time on both
// backends. On SQLite that changes nothing (its single connection serializes everything
// anyway). On Postgres, reads run in parallel across the connection pool (the main
// performance win: UI polling, feed checks, and download workers no longer queue behind
// one another), while serialized writes keep exactly the semantics the storage
// transactions were written against: several of them read a row and then write based on
// what they saw ("insert the video unless it exists"), which would otherwise race into
// unique-key errors. ytdm runs as a single process, so an in-process lock is enough.
//
// The underlying database can be swapped while the app runs (see Storage.MigrateTo):
// holding writeMu stops all writes, and a writer that was waiting picks up the new
// backend once it gets the lock.
type dbConn struct {
	cur     atomic.Pointer[backend]
	writeMu sync.Mutex
}

// backend is one open database.
type backend struct {
	db      *sql.DB
	dialect Dialect
}

// dbTx is the transaction counterpart of dbConn. It holds the write lock from Begin
// until Commit or Rollback.
type dbTx struct {
	tx      *sql.Tx
	dialect Dialect
	release func()
}

func newDBConn(db *sql.DB, dialect Dialect) *dbConn {
	c := &dbConn{}
	c.cur.Store(&backend{db: db, dialect: dialect})
	return c
}

func (c *dbConn) backend() *backend {
	return c.cur.Load()
}

// Dialect reports the current backend's dialect.
func (c *dbConn) Dialect() Dialect {
	return c.backend().dialect
}

func (c *dbConn) lockWrites() func() {
	c.writeMu.Lock()
	var once sync.Once
	return func() { once.Do(c.writeMu.Unlock) }
}

// Exec runs a statement outside a transaction. Every Exec in this package is a write.
func (c *dbConn) Exec(query string, args ...any) (sql.Result, error) {
	unlock := c.lockWrites()
	defer unlock()
	b := c.backend()
	return b.db.Exec(rebind(b.dialect, query), bindArgs(b.dialect, args)...)
}

func (c *dbConn) Query(query string, args ...any) (*sql.Rows, error) {
	b := c.backend()
	return b.db.Query(rebind(b.dialect, query), bindArgs(b.dialect, args)...)
}

func (c *dbConn) QueryRow(query string, args ...any) *sql.Row {
	b := c.backend()
	return b.db.QueryRow(rebind(b.dialect, query), bindArgs(b.dialect, args)...)
}

// Begin starts a transaction. Transactions are used for writes, so this waits for the
// write lock and holds it until Commit or Rollback.
func (c *dbConn) Begin() (*dbTx, error) {
	unlock := c.lockWrites()
	b := c.backend()
	tx, err := b.db.Begin()
	if err != nil {
		unlock()
		return nil, err
	}
	return &dbTx{tx: tx, dialect: b.dialect, release: unlock}, nil
}

func (c *dbConn) Close() error {
	return c.backend().db.Close()
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
