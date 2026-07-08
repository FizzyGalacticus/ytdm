package storage

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// applyMigrations creates the schema_migrations tracking table if needed, then applies
// every embedded migration file whose version isn't yet recorded, in ascending numeric
// order, each inside its own transaction. Safe to call on every startup.
func applyMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}

	type migration struct {
		version  int
		filename string
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionStr, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return fmt.Errorf("migration filename %q does not start with a numeric version prefix", entry.Name())
		}
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			return fmt.Errorf("migration filename %q has an invalid numeric version prefix: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{version: version, filename: entry.Name()})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	for _, m := range migrations {
		var alreadyApplied bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`, m.version).Scan(&alreadyApplied); err != nil {
			return fmt.Errorf("checking schema_migrations for version %d: %w", m.version, err)
		}
		if alreadyApplied {
			continue
		}

		contents, err := migrationFiles.ReadFile("migrations/" + m.filename)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", m.filename, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning transaction for migration %s: %w", m.filename, err)
		}
		if _, err := tx.Exec(string(contents)); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", m.filename, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", m.filename, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", m.filename, err)
		}
	}

	return nil
}
