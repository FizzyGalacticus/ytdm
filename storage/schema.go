package storage

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFiles embed.FS

// migrationLockKey is the PostgreSQL advisory lock taken while migrating, so two ytdm
// processes starting against the same database at once can't both apply a migration.
const migrationLockKey = 7_392_115_204

// migrationProbes detect a migration whose changes are already present even though
// schema_migrations doesn't record it (e.g. the record was lost in a partial restore).
// When the probe query succeeds, the version is recorded instead of re-applied, since
// statements like ALTER TABLE ... ADD COLUMN can't be repeated on SQLite.
var migrationProbes = map[int]string{
	2: `SELECT disable_notifications FROM channels LIMIT 0`,
	3: `SELECT c.sponsorblock_mark, ivt.sponsorblock_mark FROM channels c, individual_video_tracking ivt LIMIT 0`,
}

type migration struct {
	version  int
	filename string
}

// applyMigrations creates the schema_migrations tracking table if needed, then applies
// every embedded migration file for the connection's dialect whose version isn't yet
// recorded, in ascending numeric order, each inside its own transaction. Safe to call on
// every startup, after any crash, and from several processes at once:
//
//   - each migration commits atomically together with its schema_migrations record, so
//     a crash leaves it either fully applied or not at all;
//   - migration SQL is written to be re-runnable (IF NOT EXISTS, ON CONFLICT DO
//     NOTHING), and migrationProbes cover statements that can't be;
//   - on PostgreSQL an advisory lock serializes concurrent runners, and each version is
//     re-checked after the lock is taken;
//   - versions recorded by a newer ytdm are left alone with a warning.
//
// Each dialect has its own migrations/<dialect>/ directory, and the two directories must
// always carry the same version numbers producing equivalent schemas: CopyDatabase and
// MergeDatabase rely on that to move data between backends.
func applyMigrations(db *dbConn) error {
	dialect := db.Dialect()
	migrations, err := embeddedMigrations(dialect)
	if err != nil {
		return err
	}

	if err := withMigrationLock(db, func(tx *dbTx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`)
		return err
	}); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	known := map[int]bool{}
	for _, m := range migrations {
		known[m.version] = true
		if err := applyMigration(db, dir(dialect), m); err != nil {
			return err
		}
	}

	if rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`); err == nil {
		var unknown []int
		for rows.Next() {
			var v int
			if rows.Scan(&v) == nil && !known[v] {
				unknown = append(unknown, v)
			}
		}
		rows.Close()
		if len(unknown) > 0 {
			log.Printf("Warning: the %s database has schema migrations %v from a newer ytdm version; this version may not use them correctly", dialect, unknown)
		}
	}
	return nil
}

func dir(d Dialect) string {
	return "migrations/" + string(d)
}

func embeddedMigrations(d Dialect) ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, dir(d))
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionStr, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration filename %q does not start with a numeric version prefix", entry.Name())
		}
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			return nil, fmt.Errorf("migration filename %q has an invalid numeric version prefix: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{version: version, filename: entry.Name()})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

// withMigrationLock runs fn in a transaction that, on PostgreSQL, first takes the
// migration advisory lock (released automatically at commit or rollback).
func withMigrationLock(db *dbConn, fn func(tx *dbTx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if tx.dialect == DialectPostgres {
		if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, int64(migrationLockKey)); err != nil {
			return fmt.Errorf("taking migration lock: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func applyMigration(db *dbConn, migrationsDir string, m migration) error {
	applied, err := migrationRecorded(db, m.version)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}

	// A probe runs outside the migration transaction: on PostgreSQL a failed statement
	// would abort the transaction it ran in.
	alreadyPresent := false
	if probe, ok := migrationProbes[m.version]; ok {
		if rows, err := db.Query(probe); err == nil {
			rows.Close()
			alreadyPresent = true
		}
	}

	contents, err := migrationFiles.ReadFile(migrationsDir + "/" + m.filename)
	if err != nil {
		return fmt.Errorf("reading migration %s: %w", m.filename, err)
	}

	return withMigrationLock(db, func(tx *dbTx) error {
		// Re-check under the lock: another process may have applied it meanwhile.
		var recorded bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`, m.version).Scan(&recorded); err != nil {
			return fmt.Errorf("checking schema_migrations for version %d: %w", m.version, err)
		}
		if recorded {
			return nil
		}
		if alreadyPresent {
			log.Printf("Schema migration %s is already present but wasn't recorded; recording it", m.filename)
		} else if _, err := tx.Exec(string(contents)); err != nil {
			return fmt.Errorf("applying migration %s: %w", m.filename, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("recording migration %s: %w", m.filename, err)
		}
		return nil
	})
}

func migrationRecorded(db *dbConn, version int) (bool, error) {
	var applied bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`, version).Scan(&applied); err != nil {
		return false, fmt.Errorf("checking schema_migrations for version %d: %w", version, err)
	}
	return applied, nil
}
