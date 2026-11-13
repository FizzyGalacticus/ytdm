package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ytdm/storage"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDBSettingsFromEnv(t *testing.T) {
	db := map[string]string{envPostgresDatabase: "media"}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name    string
		env     map[string]string
		dialect storage.Dialect
		warnHas string // empty: no warning expected
	}{
		{"default sqlite", nil, storage.DialectSQLite, ""},
		{"database alone selects postgres", db, storage.DialectPostgres, ""},
		{"full discrete vars", with(db, envPostgresUser, "ytdm", envPostgresPassword, "pw", envPostgresHost, "pg"), storage.DialectPostgres, ""},
		{"url selects postgres", map[string]string{envPostgresURL: "postgres://u:p@h/db"}, storage.DialectPostgres, ""},
		{"explicit sqlite ignores postgres vars", with(db, envDBDriver, "sqlite"), storage.DialectSQLite, ""},
		{"user without database falls back", map[string]string{envPostgresUser: "ytdm", envPostgresPassword: "pw"}, storage.DialectSQLite, envPostgresDatabase},
		{"driver alone falls back", map[string]string{envDBDriver: "postgres"}, storage.DialectSQLite, "database named explicitly"},
		{"url without database falls back", map[string]string{envPostgresURL: "postgres://u:p@h:5432"}, storage.DialectSQLite, "database"},
		{"url and discrete vars fall back", with(db, envPostgresURL, "postgres://u:p@h/db"), storage.DialectSQLite, "not both"},
		{"bad schema falls back", with(db, envPostgresSchema, "ytdm; drop"), storage.DialectSQLite, envPostgresSchema},
		{"unknown driver falls back", map[string]string{envDBDriver: "mysql"}, storage.DialectSQLite, "unknown database driver"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := dbSettingsFromEnv(envMap(tt.env))
			if s.Dialect != tt.dialect {
				t.Errorf("dialect = %q, want %q", s.Dialect, tt.dialect)
			}
			if tt.warnHas == "" && s.Warning != "" {
				t.Errorf("unexpected warning: %s", s.Warning)
			}
			if tt.warnHas != "" && !strings.Contains(s.Warning, tt.warnHas) {
				t.Errorf("warning = %q, want it to mention %q", s.Warning, tt.warnHas)
			}
		})
	}

	s := dbSettingsFromEnv(envMap(map[string]string{envSQLitePath: "/srv/ytdm/state.db"}))
	if s.SQLitePath != "/srv/ytdm/state.db" {
		t.Errorf("SQLitePath = %q", s.SQLitePath)
	}
	if opts, _ := s.options(storage.DialectSQLite); opts.LegacyJSONPath != "/srv/ytdm/data.json" {
		t.Errorf("LegacyJSONPath = %q", opts.LegacyJSONPath)
	}
}

// TestOpenStorageFallsBackWhenPostgresUnreachable: ytdm must start without Postgres.
func TestOpenStorageFallsBackWhenPostgresUnreachable(t *testing.T) {
	prev := postgresConnectRetries
	postgresConnectRetries = []time.Duration{time.Millisecond}
	t.Cleanup(func() { postgresConnectRetries = prev })

	sqlitePath := filepath.Join(t.TempDir(), "data.db")
	s := dbSettingsFromEnv(envMap(map[string]string{
		envPostgresHost: "127.0.0.1", envPostgresPort: "1", envPostgresDatabase: "media",
		envPostgresSSLMode: "disable", envSQLitePath: sqlitePath,
	}))
	if s.Dialect != storage.DialectPostgres {
		t.Fatalf("expected postgres to be selected, got %q (%s)", s.Dialect, s.Warning)
	}
	var logged []string
	store, err := openStorage(&s, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	if err != nil {
		t.Fatalf("openStorage() error = %v", err)
	}
	defer store.Close()
	if store.Dialect() != storage.DialectSQLite || s.Dialect != storage.DialectSQLite {
		t.Errorf("expected SQLite fallback, got store %q / settings %q", store.Dialect(), s.Dialect)
	}
	if !strings.Contains(s.Warning, "using SQLite") || len(logged) != 1 {
		t.Errorf("warning = %q, retries logged = %d", s.Warning, len(logged))
	}
}

func TestPostgresDSNIsExplicit(t *testing.T) {
	p := postgresSettings{User: "yt dm", Password: `it's\secret`, Database: "media", Host: "db", SSLMode: "disable", Schema: "ytdm"}
	dsn, err := p.dsn()
	if err != nil {
		t.Fatal(err)
	}
	want := `host=db port=5432 dbname=media user='yt dm' password='it\'s\\secret' sslmode=disable search_path=ytdm`
	if dsn != want {
		t.Errorf("dsn =\n%s\nwant\n%s", dsn, want)
	}

	// User and password are optional; the database never is.
	if dsn, err := (postgresSettings{Database: "media"}).dsn(); err != nil || dsn != "host=localhost port=5432 dbname=media" {
		t.Errorf("minimal dsn = %q, %v", dsn, err)
	}

	u, err := postgresSettings{URL: "postgres://u:p@h:5432/db?sslmode=disable", Schema: "ytdm"}.dsn()
	if err != nil || !strings.Contains(u, "search_path=ytdm") || !strings.Contains(u, "/db?") {
		t.Errorf("url dsn = %q, %v", u, err)
	}
}

func TestRedactDSN(t *testing.T) {
	tests := map[string]string{
		"postgres://user:hunter2@db:5432/ytdm?sslmode=disable": "postgres://user:***@db:5432/ytdm?sslmode=disable",
		"postgres://user@db/ytdm":                              "postgres://user@db/ytdm",
		"host=db user=u password=hunter2 dbname=ytdm":          "host=db user=u password=*** dbname=ytdm",
	}
	for in, want := range tests {
		if got := redactDSN(in); got != want {
			t.Errorf("redactDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunMigrateDBRequiresDestination(t *testing.T) {
	var out bytes.Buffer
	if err := runMigrateDB(nil, envMap(nil), &out); err == nil {
		t.Fatal("expected an error without --to")
	}
	if !strings.Contains(out.String(), "Usage: ytdm migrate-db") {
		t.Errorf("expected usage text, got %q", out.String())
	}
	if err := runMigrateDB([]string{"--to", "postgres"}, envMap(nil), &out); err == nil || !strings.Contains(err.Error(), "no PostgreSQL connection configured") {
		t.Errorf("expected a missing connection error, got %v", err)
	}
	if err := runMigrateDB([]string{"--to", "postgres"}, envMap(map[string]string{envPostgresUser: "u"}), &out); err == nil || !strings.Contains(err.Error(), "database named explicitly") {
		t.Errorf("expected a missing database error, got %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if err := runMigrateDB([]string{"--to", "postgres", "--sqlite-path", missing}, envMap(map[string]string{envPostgresDatabase: "media"}), &out); err == nil || !strings.Contains(err.Error(), "missing.db") {
		t.Errorf("expected a missing source error, got %v", err)
	}
}
