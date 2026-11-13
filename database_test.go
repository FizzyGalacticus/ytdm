package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
			env := map[string]string{envSQLitePath: filepath.Join(t.TempDir(), "data.db")}
			for k, v := range tt.env {
				env[k] = v
			}
			s := dbSettingsFromEnv(envMap(env))
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

// TestOpenStorageFallsBackWhenPostgresUnreachable: ytdm must start without Postgres,
// on the separate fallback database rather than the main SQLite file.
func TestOpenStorageFallsBackWhenPostgresUnreachable(t *testing.T) {
	prev := postgresConnectRetries
	postgresConnectRetries = []time.Duration{time.Millisecond}
	t.Cleanup(func() { postgresConnectRetries = prev })

	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "data.db")
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
	if store.Dialect() != storage.DialectSQLite || !s.UsingFallback {
		t.Errorf("expected SQLite fallback, got store %q / fallback %v", store.Dialect(), s.UsingFallback)
	}
	if err := store.AddChannel(storage.Channel{ID: "UCfb", URL: "u", Name: "FB"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fallback.db")); err != nil {
		t.Errorf("fallback database not created: %v", err)
	}
	if _, err := os.Stat(sqlitePath); err == nil {
		t.Error("the main SQLite file must not be used as the fallback")
	}
	if !strings.Contains(s.Warning, "fallback") || len(logged) != 1 {
		t.Errorf("warning = %q, retries logged = %d", s.Warning, len(logged))
	}
}

func TestBackendSelectionPrecedence(t *testing.T) {
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "data.db")
	pg := map[string]string{envPostgresDatabase: "media", envSQLitePath: sqlitePath}

	// Postgres configured and no SQLite data yet: Postgres.
	if s := dbSettingsFromEnv(envMap(pg)); s.Dialect != storage.DialectPostgres {
		t.Errorf("fresh install with postgres configured: %q", s.Dialect)
	}

	// Existing SQLite data and no preference: keep SQLite, with a notice.
	if err := os.WriteFile(sqlitePath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	s := dbSettingsFromEnv(envMap(pg))
	if s.Dialect != storage.DialectSQLite || !strings.Contains(s.Notice, "migrate") {
		t.Errorf("existing sqlite data should stay in use until migrated: %q / %q", s.Dialect, s.Notice)
	}

	// A saved preference wins over auto-detection...
	if err := s.savePreference(storage.DialectPostgres); err != nil {
		t.Fatal(err)
	}
	s = dbSettingsFromEnv(envMap(pg))
	if s.Dialect != storage.DialectPostgres || s.PreferredPostgres != "localhost:5432/media" {
		t.Errorf("preference not honored: %q (%q)", s.Dialect, s.PreferredPostgres)
	}
	// ...and YTDM_DB_DRIVER wins over the preference.
	pinned := map[string]string{envDBDriver: "sqlite"}
	for k, v := range pg {
		pinned[k] = v
	}
	if s := dbSettingsFromEnv(envMap(pinned)); s.Dialect != storage.DialectSQLite || s.Pinned != storage.DialectSQLite {
		t.Errorf("YTDM_DB_DRIVER should win: %q", s.Dialect)
	}

	// A Postgres preference whose settings vanished falls back to the fallback database.
	s = dbSettingsFromEnv(envMap(map[string]string{envSQLitePath: sqlitePath}))
	if s.Dialect != storage.DialectSQLite || !s.UsingFallback {
		t.Errorf("missing postgres settings with a postgres preference: %q fallback=%v", s.Dialect, s.UsingFallback)
	}

	// A corrupt preference file is ignored, not fatal.
	if err := os.WriteFile(filepath.Join(dir, "database.json"), []byte("{nope"), 0644); err != nil {
		t.Fatal(err)
	}
	if s := dbSettingsFromEnv(envMap(pg)); s.Preferred != "" {
		t.Errorf("corrupt preference should be ignored, got %q", s.Preferred)
	}
}

// postgresTestEnv returns env settings for a fresh, uniquely named schema on the test
// server, or skips the test when YTDM_TEST_POSTGRES_URL isn't set.
func postgresTestEnv(t *testing.T, sqlitePath, schema string) map[string]string {
	t.Helper()
	base := os.Getenv("YTDM_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("YTDM_TEST_POSTGRES_URL not set")
	}
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a, err := sql.Open("postgres", base); err == nil {
			a.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
			a.Close()
		}
	})
	return map[string]string{envPostgresURL: base, envPostgresSchema: schema, envSQLitePath: sqlitePath}
}

func TestFallbackDataIsReconciledWhenPostgresReturns(t *testing.T) {
	dir := t.TempDir()
	env := postgresTestEnv(t, filepath.Join(dir, "data.db"), "ytdm_test_reconcile")

	// First start: Postgres works and gets a channel.
	s := dbSettingsFromEnv(envMap(env))
	store, err := openStorage(&s, t.Logf)
	if err != nil || store.Dialect() != storage.DialectPostgres {
		t.Fatalf("first start: %v %v", err, s.Warning)
	}
	if err := store.AddChannel(storage.Channel{ID: "UCpg", URL: "u", Name: "PG"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// An outage: ytdm ran on the fallback and a channel was added there.
	fb, err := storage.Open(storage.Options{Dialect: storage.DialectSQLite, SQLitePath: filepath.Join(dir, "fallback.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := fb.AddChannel(storage.Channel{ID: "UCoutage", URL: "u", Name: "Outage"}); err != nil {
		t.Fatal(err)
	}
	fb.Close()

	// Postgres is back: the outage data is merged and the fallback retired.
	s = dbSettingsFromEnv(envMap(env))
	store, err = openStorage(&s, t.Logf)
	if err != nil || store.Dialect() != storage.DialectPostgres {
		t.Fatalf("restart: %v %v", err, s.Warning)
	}
	defer store.Close()
	if !store.HasChannel("UCpg") || !store.HasChannel("UCoutage") {
		t.Error("expected both the original and the outage channel in Postgres")
	}
	if _, err := os.Stat(filepath.Join(dir, "fallback.db")); err == nil {
		t.Error("fallback database should be retired after merging")
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "fallback.db.merged-*"))
	if len(backups) == 0 {
		t.Error("the merged fallback should be kept as a backup")
	}
	if s.Warning != "" {
		t.Errorf("unexpected warning: %s", s.Warning)
	}
}

func TestChangedPostgresConnectionToEmptyDatabaseIsReported(t *testing.T) {
	dir := t.TempDir()
	env := postgresTestEnv(t, filepath.Join(dir, "data.db"), "ytdm_test_ident_a")
	s := dbSettingsFromEnv(envMap(env))
	store, err := openStorage(&s, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddChannel(storage.Channel{ID: "UCa", URL: "u", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	env2 := postgresTestEnv(t, filepath.Join(dir, "data.db"), "ytdm_test_ident_b")
	s = dbSettingsFromEnv(envMap(env2))
	store, err = openStorage(&s, t.Logf)
	if err != nil || store.Dialect() != storage.DialectPostgres {
		t.Fatalf("start on new connection: %v", err)
	}
	store.Close()
	if !strings.Contains(s.Notice, "ytdm_test_ident_a") || !strings.Contains(s.Notice, "no channels or videos") {
		t.Errorf("expected a notice about the empty new database, got %q", s.Notice)
	}
	// The original location stays recorded until data arrives.
	if pref := readDBPreference(s.preferencePath()); !strings.Contains(pref.Postgres, "ytdm_test_ident_a") {
		t.Errorf("preference should still point at the original database: %+v", pref)
	}
}

func TestDatabaseMigrateAPI(t *testing.T) {
	dir := t.TempDir()
	env := postgresTestEnv(t, filepath.Join(dir, "data.db"), "ytdm_test_api_migrate")

	// Running on SQLite with some data; Postgres configured but not yet used.
	seed, err := storage.Open(storage.Options{Dialect: storage.DialectSQLite, SQLitePath: env[envSQLitePath]})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AddChannel(storage.Channel{ID: "UClive", URL: "u", Name: "Live"}); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	s := dbSettingsFromEnv(envMap(env))
	if s.Dialect != storage.DialectSQLite {
		t.Fatalf("existing sqlite data should stay in use, got %q", s.Dialect)
	}
	store, err := openStorage(&s, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prevManager := dbManager
	dbManager = &databaseManager{}
	dbManager.set(s)
	t.Cleanup(func() { dbManager = prevManager })
	api := &APIServer{config: DefaultConfig(), storage: store}

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.handleDatabaseMigrate(rec, httptest.NewRequest(http.MethodPost, "/api/database/migrate", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"to":"sqlite"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("migrating to the active backend should fail: %d", rec.Code)
	}
	if rec := post(`{"to":"postgres","mode":"sideways"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad mode should fail: %d", rec.Code)
	}
	if rec := post(`{"to":"postgres","mode":"merge"}`); rec.Code != http.StatusOK {
		t.Fatalf("migrate to postgres = %d %s", rec.Code, rec.Body.String())
	}
	if store.Dialect() != storage.DialectPostgres || dbManager.get().Dialect != storage.DialectPostgres {
		t.Error("live store should now run on Postgres")
	}
	if err := store.AddChannel(storage.Channel{ID: "UCafter", URL: "u", Name: "After"}); err != nil {
		t.Fatal(err)
	}
	if pref := readDBPreference(s.preferencePath()); pref.Driver != storage.DialectPostgres {
		t.Errorf("preference not saved: %+v", pref)
	}

	// The next start opens Postgres, with both channels.
	next := dbSettingsFromEnv(envMap(env))
	reopened, err := openStorage(&next, t.Logf)
	if err != nil || reopened.Dialect() != storage.DialectPostgres {
		t.Fatalf("restart after migration: %v %q", err, next.Warning)
	}
	defer reopened.Close()
	if !reopened.HasChannel("UClive") || !reopened.HasChannel("UCafter") {
		t.Error("restart should see migrated and post-migration data")
	}

	// And back to SQLite, merging into the existing data.db.
	if rec := post(`{"to":"sqlite"}`); rec.Code != http.StatusOK {
		t.Fatalf("migrate back to sqlite = %d %s", rec.Code, rec.Body.String())
	}
	if store.Dialect() != storage.DialectSQLite || !store.HasChannel("UCafter") {
		t.Error("expected to be back on SQLite with the Postgres-era channel")
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
