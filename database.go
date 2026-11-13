package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ytdm/storage"
)

// Database selection environment variables.
const (
	envDBDriver   = "YTDM_DB_DRIVER"
	envSQLitePath = "YTDM_SQLITE_PATH"

	// Postgres connection: either a full URL, or the discrete variables below. The
	// database name must always be given explicitly; ytdm never falls back to a libpq
	// default database (such as one named after the user).
	envPostgresURL      = "YTDM_POSTGRES_URL"
	envPostgresHost     = "YTDM_POSTGRES_HOST"
	envPostgresPort     = "YTDM_POSTGRES_PORT"
	envPostgresUser     = "YTDM_POSTGRES_USER"
	envPostgresPassword = "YTDM_POSTGRES_PASSWORD"
	envPostgresDatabase = "YTDM_POSTGRES_DATABASE"
	envPostgresSSLMode  = "YTDM_POSTGRES_SSLMODE"
	// envPostgresSchema optionally keeps ytdm's tables in their own schema instead of
	// the database's default search_path (normally "public"). Created if missing.
	envPostgresSchema = "YTDM_POSTGRES_SCHEMA"

	defaultSQLitePath = "data/data.db"
)

// postgresSettings is the Postgres connection configuration.
type postgresSettings struct {
	URL      string
	Host     string
	Port     string
	User     string
	Password string
	Database string
	SSLMode  string
	Schema   string
}

func postgresSettingsFromEnv(getenv func(string) string) postgresSettings {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	return postgresSettings{
		URL:      get(envPostgresURL),
		Host:     get(envPostgresHost),
		Port:     get(envPostgresPort),
		User:     get(envPostgresUser),
		Password: getenv(envPostgresPassword), // passwords may legitimately have edge spaces
		Database: get(envPostgresDatabase),
		SSLMode:  get(envPostgresSSLMode),
		Schema:   get(envPostgresSchema),
	}
}

// configured reports whether any Postgres connection setting was provided.
func (p postgresSettings) configured() bool {
	return p.URL != "" || p.Host != "" || p.Port != "" || p.User != "" || p.Password != "" || p.Database != ""
}

func (p postgresSettings) hasDiscrete() bool {
	return p.Host != "" || p.Port != "" || p.User != "" || p.Password != "" || p.Database != "" || p.SSLMode != ""
}

var postgresSchemaPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// dsn validates the settings and builds the connection string handed to the driver.
// The database name is mandatory whichever form is used.
func (p postgresSettings) dsn() (string, error) {
	if p.Schema != "" && !postgresSchemaPattern.MatchString(p.Schema) {
		return "", fmt.Errorf("%s must be a plain identifier (letters, digits, underscores)", envPostgresSchema)
	}

	if p.URL != "" {
		if p.hasDiscrete() {
			return "", fmt.Errorf("set either %s or the individual %s_* variables, not both", envPostgresURL, "YTDM_POSTGRES")
		}
		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
			return "", fmt.Errorf("%s must be a postgres://user:password@host:port/database URL", envPostgresURL)
		}
		if strings.Trim(u.Path, "/") == "" {
			return "", fmt.Errorf("%s must name the database explicitly (postgres://user:password@host:port/database)", envPostgresURL)
		}
		if p.Schema != "" {
			q := u.Query()
			q.Set("search_path", p.Schema)
			u.RawQuery = q.Encode()
		}
		return u.String(), nil
	}

	if p.Database == "" {
		return "", fmt.Errorf("postgres needs the database named explicitly: set %s (or %s)", envPostgresDatabase, envPostgresURL)
	}
	host, port := p.Host, p.Port
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("%s must be a number, got %q", envPostgresPort, port)
	}
	parts := []string{
		"host=" + quoteDSNValue(host),
		"port=" + port,
		"dbname=" + quoteDSNValue(p.Database),
	}
	if p.User != "" {
		parts = append(parts, "user="+quoteDSNValue(p.User))
	}
	if p.Password != "" {
		parts = append(parts, "password="+quoteDSNValue(p.Password))
	}
	if p.SSLMode != "" {
		parts = append(parts, "sslmode="+quoteDSNValue(p.SSLMode))
	}
	if p.Schema != "" {
		parts = append(parts, "search_path="+p.Schema)
	}
	return strings.Join(parts, " "), nil
}

// quoteDSNValue quotes a value for a libpq key=value connection string.
func quoteDSNValue(v string) string {
	if v != "" && !strings.ContainsAny(v, ` '\`) {
		return v
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func (p postgresSettings) describe() string {
	var where string
	if p.URL != "" {
		where = redactDSN(p.URL)
	} else {
		host, port := p.Host, p.Port
		if host == "" {
			host = "localhost"
		}
		if port == "" {
			port = "5432"
		}
		where = fmt.Sprintf("%s:%s/%s", host, port, p.Database)
		if p.User != "" {
			where = p.User + "@" + where
		}
	}
	if p.Schema != "" {
		where += ", schema " + p.Schema
	}
	return "postgres (" + where + ")"
}

// dbSettings is the database configuration resolved from the environment and the
// saved backend preference.
type dbSettings struct {
	Dialect    storage.Dialect
	SQLitePath string
	Postgres   postgresSettings
	// Pinned is set when YTDM_DB_DRIVER chose the backend; it overrides the saved
	// preference, so the UI won't switch away from it.
	Pinned storage.Dialect
	// Preferred is the backend saved in database.json by a previous migration or
	// startup, if any; PreferredPostgres identifies the Postgres database that held
	// the data then (see postgresSettings.identity).
	Preferred         storage.Dialect
	PreferredPostgres string
	// UsingFallback is set when Postgres was intended but ytdm runs on the temporary
	// fallback SQLite database instead (see fallbackPath).
	UsingFallback bool
	// Warning explains why ytdm is not using the backend that was asked for.
	Warning string
	// Notice is informational (e.g. Postgres is configured but existing SQLite data is
	// still in use until it is migrated).
	Notice string
}

// dbPreference is the content of data/database.json.
type dbPreference struct {
	Driver    storage.Dialect `json:"driver"`
	Postgres  string          `json:"postgres,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// fallbackPath is the SQLite database ytdm runs on while an intended Postgres is
// unavailable. It is deliberately separate from the main SQLite file: that file may
// hold data from before a migration to Postgres, and running on it would resurrect
// deleted channels or re-download videos Postgres knows were already handled. Anything
// written to the fallback is merged into Postgres once it is back (see
// reconcileFallback).
func (s dbSettings) fallbackPath() string {
	return filepath.Join(filepath.Dir(s.SQLitePath), "fallback.db")
}

// identity names the Postgres database holding ytdm's data (host, port, database,
// schema; not the user or password), to notice when the connection settings point
// somewhere else after a restart.
func (p postgresSettings) identity() string {
	host, port, db := p.Host, p.Port, p.Database
	if p.URL != "" {
		if u, err := url.Parse(p.URL); err == nil {
			host, port, db = u.Hostname(), u.Port(), strings.Trim(u.Path, "/")
		}
	}
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}
	id := fmt.Sprintf("%s:%s/%s", host, port, db)
	if p.Schema != "" {
		id += " (schema " + p.Schema + ")"
	}
	return id
}

// preferencePath is where the backend preference lives: next to the SQLite file, in
// the data directory.
func (s dbSettings) preferencePath() string {
	return filepath.Join(filepath.Dir(s.SQLitePath), "database.json")
}

// readDBPreference returns the saved preference; a missing or unreadable file is
// treated as no preference.
func readDBPreference(path string) dbPreference {
	raw, err := os.ReadFile(path)
	if err != nil {
		return dbPreference{}
	}
	var p dbPreference
	if json.Unmarshal(raw, &p) != nil {
		return dbPreference{}
	}
	d, err := parseDialect(string(p.Driver))
	if err != nil {
		return dbPreference{}
	}
	p.Driver = d
	return p
}

// saveDBPreference records which backend ytdm should open on its next start.
func (s *dbSettings) savePreference(d storage.Dialect) error {
	pref := dbPreference{Driver: d, UpdatedAt: time.Now().UTC()}
	if d == storage.DialectPostgres {
		pref.Postgres = s.Postgres.identity()
	}
	raw, err := json.MarshalIndent(pref, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename so a crash mid-write never leaves a corrupt preference.
	tmp := s.preferencePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.preferencePath()); err != nil {
		return err
	}
	s.Preferred = d
	s.PreferredPostgres = pref.Postgres
	return nil
}

// dbSettingsFromEnv resolves which backend to use, in order:
//
//  1. YTDM_DB_DRIVER, when set.
//  2. The preference saved in data/database.json (written when the database is
//     migrated, or the first time ytdm starts on Postgres).
//  3. Postgres when it is configured, unless an existing SQLite database is present:
//     then SQLite stays in use (with a Notice) until the data is migrated, so adding
//     Postgres settings never strands existing data.
//  4. SQLite.
//
// It never fails: ytdm always runs, so invalid Postgres settings (or an unknown driver)
// fall back to SQLite with a Warning.
func dbSettingsFromEnv(getenv func(string) string) dbSettings {
	s := dbSettings{
		SQLitePath: strings.TrimSpace(getenv(envSQLitePath)),
		Postgres:   postgresSettingsFromEnv(getenv),
		Dialect:    storage.DialectSQLite,
	}
	if s.SQLitePath == "" {
		s.SQLitePath = defaultSQLitePath
	}
	pref := readDBPreference(s.preferencePath())
	s.Preferred, s.PreferredPostgres = pref.Driver, pref.Postgres

	dialect, err := parseDialect(getenv(envDBDriver))
	if err != nil {
		s.Warning = fmt.Sprintf("%s: %v; using SQLite", envDBDriver, err)
		return s
	}
	s.Pinned = dialect
	if dialect == "" {
		dialect = s.Preferred
	}
	if dialect == "" && s.Postgres.configured() {
		if _, err := os.Stat(s.SQLitePath); err == nil {
			s.Notice = "PostgreSQL is configured, but ytdm keeps using the existing SQLite database until you migrate it (Configuration > Database)."
			return s
		}
		dialect = storage.DialectPostgres
	}
	if dialect == storage.DialectPostgres {
		if _, err := s.Postgres.dsn(); err != nil {
			s.Warning = fmt.Sprintf("PostgreSQL settings are invalid (%v); running on the temporary fallback database %s until they're fixed", err, s.fallbackPath())
			s.UsingFallback = true
			return s
		}
		s.Dialect = storage.DialectPostgres
	}
	return s
}

// postgresConnectRetries are the waits between Postgres connection attempts at
// startup, so a database container that is still booting isn't mistaken for a
// missing one.
var postgresConnectRetries = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// openStorage opens the configured backend. When Postgres is intended but can't be
// opened, ytdm runs on the temporary fallback SQLite database and records why in
// s.Warning, so it always starts. After Postgres opens, anything written to the
// fallback during an earlier outage is merged in. Only a SQLite failure is an error.
func openStorage(s *dbSettings, logf func(format string, args ...any)) (*storage.Storage, error) {
	if s.Dialect == storage.DialectPostgres {
		opts, err := s.options(storage.DialectPostgres)
		if err == nil {
			for attempt := 0; ; attempt++ {
				var store *storage.Storage
				store, err = storage.Open(opts)
				if err == nil {
					healAfterPostgresStart(store, s, logf)
					return store, nil
				}
				if attempt >= len(postgresConnectRetries) {
					break
				}
				logf("PostgreSQL not ready (%v); retrying in %s", err, postgresConnectRetries[attempt])
				time.Sleep(postgresConnectRetries[attempt])
			}
		}
		s.Warning = fmt.Sprintf("could not open %s: %v; running on the temporary fallback database %s until it's reachable (changes made meanwhile are merged into PostgreSQL on the next start)", s.Postgres.describe(), err, s.fallbackPath())
		s.Dialect = storage.DialectSQLite
		s.UsingFallback = true
	}

	opts, err := s.options(storage.DialectSQLite)
	if err != nil {
		return nil, err
	}
	if s.UsingFallback {
		opts.SQLitePath = s.fallbackPath()
		opts.LegacyJSONPath = ""
	}
	return storage.Open(opts)
}

// healAfterPostgresStart runs once Postgres is open: it folds in any fallback data,
// notices when the connection now points at a different (empty) database, and records
// the preference. Failures here are reported but never stop startup.
func healAfterPostgresStart(store *storage.Storage, s *dbSettings, logf func(format string, args ...any)) {
	if err := reconcileFallback(store, *s, logf); err != nil {
		s.Warning = fmt.Sprintf("couldn't merge the fallback database %s into PostgreSQL (%v); it was left in place and will be retried on the next start", s.fallbackPath(), err)
	}

	current := s.Postgres.identity()
	if s.PreferredPostgres != "" && s.PreferredPostgres != current {
		if empty, err := store.IsEmpty(); err == nil && empty {
			// Keep the old identity recorded so this stays visible until data arrives.
			s.Notice = fmt.Sprintf("Connected to PostgreSQL %s, which has no channels or videos yet. ytdm's data was previously in %s; check the connection settings if that's unexpected, or migrate data in from Configuration > Database.", current, s.PreferredPostgres)
			return
		}
		logf("PostgreSQL connection changed from %s to %s", s.PreferredPostgres, current)
	}
	if s.Pinned == "" && (s.Preferred != storage.DialectPostgres || s.PreferredPostgres != current) {
		if err := s.savePreference(storage.DialectPostgres); err != nil {
			logf("Warning: failed to save database preference: %v", err)
		}
	}
}

// reconcileFallback merges the fallback SQLite database (written during an earlier
// Postgres outage) into Postgres, then keeps it as a timestamped backup so it is never
// merged twice.
func reconcileFallback(store *storage.Storage, s dbSettings, logf func(format string, args ...any)) error {
	path := s.fallbackPath()
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	fb, err := storage.Open(storage.Options{Dialect: storage.DialectSQLite, SQLitePath: path})
	if err != nil {
		return err
	}
	empty, err := fb.IsEmpty()
	if err == nil && !empty {
		var result storage.CopyResult
		if result, err = storage.ReconcileInto(fb, store); err == nil {
			var added, advanced int64
			for _, tc := range result.Tables {
				if tc.Table == "channels" || tc.Table == "videos" {
					added += tc.Inserted
				}
				if tc.Table == "videos" {
					advanced += tc.Updated
				}
			}
			logf("Merged the fallback database into PostgreSQL: %d channel/video row(s) added, %d video status(es) advanced", added, advanced)
		}
	}
	fb.Close()
	if err != nil {
		return err
	}
	return retireFallback(path)
}

// retireFallback renames the fallback database (and any SQLite side files) out of the
// way, keeping it as a backup.
func retireFallback(path string) error {
	suffix := ".merged-" + time.Now().UTC().Format("20060102T150405Z")
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + side); err == nil {
			os.Rename(path+side, path+suffix+side)
		}
	}
	return os.Rename(path, path+suffix)
}

func parseDialect(v string) (storage.Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return "", nil
	case "sqlite", "sqlite3":
		return storage.DialectSQLite, nil
	case "postgres", "postgresql", "pg":
		return storage.DialectPostgres, nil
	default:
		return "", fmt.Errorf("unknown database driver %q (use sqlite or postgres)", v)
	}
}

// options builds storage.Options for the given dialect. The legacy data.json import
// always looks next to the SQLite path, where pre-SQL ytdm kept it.
func (s dbSettings) options(dialect storage.Dialect) (storage.Options, error) {
	opts := storage.Options{
		Dialect:        dialect,
		SQLitePath:     s.SQLitePath,
		LegacyJSONPath: filepath.Join(filepath.Dir(s.SQLitePath), "data.json"),
	}
	if dialect == storage.DialectPostgres {
		dsn, err := s.Postgres.dsn()
		if err != nil {
			return opts, err
		}
		opts.PostgresDSN = dsn
		opts.PostgresSchema = s.Postgres.Schema
	}
	return opts, nil
}

func (s dbSettings) describe(dialect storage.Dialect) string {
	if dialect == storage.DialectPostgres {
		return s.Postgres.describe()
	}
	return "sqlite (" + s.SQLitePath + ")"
}

// redactDSN hides the password in a postgres:// URL for logging.
func redactDSN(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			userinfo := rest[:at]
			if colon := strings.Index(userinfo, ":"); colon >= 0 {
				return dsn[:i+3] + userinfo[:colon] + ":***" + rest[at:]
			}
		}
		return dsn
	}
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if strings.HasPrefix(strings.ToLower(f), "password=") {
			fields[i] = "password=***"
		}
	}
	return strings.Join(fields, " ")
}

// databaseManager tracks the live database for the API and serializes migrations.
type databaseManager struct {
	mu       sync.Mutex // held for the duration of a migration
	stateMu  sync.RWMutex
	settings dbSettings
}

// dbManager is set up by main once storage is open.
var dbManager = &databaseManager{}

func (m *databaseManager) set(s dbSettings) {
	m.stateMu.Lock()
	m.settings = s
	m.stateMu.Unlock()
}

func (m *databaseManager) get() dbSettings {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.settings
}

// databaseInfo is the GET /api/database payload.
type databaseInfo struct {
	Active             storage.Dialect `json:"active"`
	SQLitePath         string          `json:"sqlite_path"`
	PostgresConfigured bool            `json:"postgres_configured"`
	Postgres           string          `json:"postgres,omitempty"`
	PostgresError      string          `json:"postgres_error,omitempty"`
	PinnedDriver       storage.Dialect `json:"pinned_driver,omitempty"`
	Fallback           bool            `json:"fallback,omitempty"`
	ActivePath         string          `json:"active_path,omitempty"`
	Warning            string          `json:"warning,omitempty"`
	Notice             string          `json:"notice,omitempty"`
}

func (m *databaseManager) info() databaseInfo {
	s := m.get()
	info := databaseInfo{
		Active:             s.Dialect,
		SQLitePath:         s.SQLitePath,
		PostgresConfigured: s.Postgres.configured(),
		PinnedDriver:       s.Pinned,
		Fallback:           s.UsingFallback,
		Warning:            s.Warning,
		Notice:             s.Notice,
	}
	switch {
	case s.UsingFallback:
		info.ActivePath = s.fallbackPath()
	case s.Dialect == storage.DialectSQLite:
		info.ActivePath = s.SQLitePath
	}
	if info.PostgresConfigured {
		info.Postgres = s.Postgres.describe()
		if _, err := s.Postgres.dsn(); err != nil {
			info.PostgresError = err.Error()
		}
	}
	return info
}

// errMigrationRunning is returned when a migration is requested while one runs.
var errMigrationRunning = errors.New("a database migration is already running")

// migrate moves the live store to the other backend with mode, then saves the choice
// so the next start uses it too.
func (m *databaseManager) migrate(store *storage.Storage, to storage.Dialect, mode storage.CopyMode) (storage.CopyResult, error) {
	if !m.mu.TryLock() {
		return storage.CopyResult{}, errMigrationRunning
	}
	defer m.mu.Unlock()

	s := m.get()
	if to == s.Dialect {
		return storage.CopyResult{}, fmt.Errorf("ytdm is already using %s", dialectLabel(to))
	}
	if s.Pinned != "" && s.Pinned != to {
		return storage.CopyResult{}, fmt.Errorf("%s=%s is set, so ytdm would go back to %s on its next start; remove it to switch databases from the UI", envDBDriver, s.Pinned, dialectLabel(s.Pinned))
	}
	if to == storage.DialectPostgres {
		if !s.Postgres.configured() {
			return storage.CopyResult{}, fmt.Errorf("PostgreSQL isn't configured: set %s and the other YTDM_POSTGRES_* variables (or %s), then restart", envPostgresDatabase, envPostgresURL)
		}
		if _, err := s.Postgres.dsn(); err != nil {
			return storage.CopyResult{}, err
		}
	}
	opts, err := s.options(to)
	if err != nil {
		return storage.CopyResult{}, err
	}
	opts.LegacyJSONPath = "" // the legacy import belongs to the original database only

	// Record the new database before switching to it, so a crash right after the
	// switch can't send the next start back to the old, now stale, database. If the
	// preference can't be saved, nothing switches.
	result, err := store.MigrateTo(opts, mode, func() error { return s.savePreference(to) })
	if err != nil {
		return result, err
	}

	if s.UsingFallback {
		// Its data now lives in the target; retire it so the next start doesn't merge
		// it again (which could resurrect channels deleted after this migration).
		if err := retireFallback(s.fallbackPath()); err != nil {
			log.Printf("Warning: failed to retire fallback database %s: %v", s.fallbackPath(), err)
		}
		s.UsingFallback = false
	}
	s.Dialect = to
	s.Warning = ""
	s.Notice = ""
	m.set(s)
	return result, nil
}

func dialectLabel(d storage.Dialect) string {
	if d == storage.DialectPostgres {
		return "PostgreSQL"
	}
	return "SQLite"
}

// runMigrateDB implements `ytdm migrate-db`: it copies all data from one backend into
// the other so ytdm can be moved from SQLite to Postgres or back.
func runMigrateDB(args []string, getenv func(string) string, out io.Writer) error {
	settings := dbSettingsFromEnv(getenv)

	fs := flag.NewFlagSet("migrate-db", flag.ContinueOnError)
	fs.SetOutput(out)
	to := fs.String("to", "", "destination backend: postgres or sqlite (the other one is the source)")
	fs.StringVar(&settings.SQLitePath, "sqlite-path", settings.SQLitePath, "SQLite database file (default from "+envSQLitePath+")")
	fs.StringVar(&settings.Postgres.URL, "postgres-url", settings.Postgres.URL, "postgres://user:password@host:port/database URL (default from "+envPostgresURL+" or the YTDM_POSTGRES_* variables)")
	modeFlag := fs.String("mode", "merge", "merge: keep the destination's data and upsert the source's by YouTube ID; replace: wipe the destination first")
	overwrite := fs.Bool("overwrite", false, "same as --mode replace")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: ytdm migrate-db --to postgres|sqlite [flags]")
		fmt.Fprintln(out, "\nCopies every channel and video from one database backend to the other, then")
		fmt.Fprintln(out, "makes the destination the database ytdm opens on its next start.")
		fmt.Fprintln(out, "Stop the running ytdm service first so no writes are missed.")
		fmt.Fprintln(out)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	dst, err := parseDialect(*to)
	if err != nil || dst == "" {
		fs.Usage()
		return errors.New("--to must be postgres or sqlite")
	}
	mode, err := storage.ParseCopyMode(*modeFlag)
	if err != nil {
		return err
	}
	if *overwrite {
		mode = storage.CopyReplace
	}
	src := storage.DialectSQLite
	if dst == storage.DialectSQLite {
		src = storage.DialectPostgres
	}

	// Unlike startup, an explicit migration must not fall back: bad Postgres settings
	// are an error here.
	if !settings.Postgres.configured() {
		return fmt.Errorf("no PostgreSQL connection configured: set %s or the YTDM_POSTGRES_* variables, or pass --postgres-url", envPostgresURL)
	}
	if _, err := settings.Postgres.dsn(); err != nil {
		return err
	}
	if src == storage.DialectSQLite {
		if _, err := os.Stat(settings.SQLitePath); err != nil {
			return fmt.Errorf("source SQLite database %s: %w", settings.SQLitePath, err)
		}
	}

	fmt.Fprintf(out, "Source:      %s\n", settings.describe(src))
	fmt.Fprintf(out, "Destination: %s\n", settings.describe(dst))

	srcOpts, err := settings.options(src)
	if err != nil {
		return err
	}
	// Only the source may pick up a legacy data.json; importing it into the destination
	// too would make the destination non-empty before the copy.
	dstOpts, err := settings.options(dst)
	if err != nil {
		return err
	}
	dstOpts.LegacyJSONPath = ""

	srcStore, err := storage.Open(srcOpts)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcStore.Close()

	dstStore, err := storage.Open(dstOpts)
	if err != nil {
		return fmt.Errorf("opening destination: %w", err)
	}
	defer dstStore.Close()

	result, err := storage.Transfer(srcStore, dstStore, mode)
	if err != nil {
		return err
	}
	for _, tc := range result.Tables {
		fmt.Fprintf(out, "  %-26s %d row(s): %d inserted, %d updated\n", tc.Table, tc.Rows, tc.Inserted, tc.Updated)
	}
	if err := settings.savePreference(dst); err != nil {
		fmt.Fprintf(out, "Copied, but couldn't save the backend choice to %s: %v\n", settings.preferencePath(), err)
	} else {
		fmt.Fprintf(out, "Done. ytdm will use %s on its next start.\n", dialectLabel(dst))
	}
	if settings.Pinned != "" && settings.Pinned != dst {
		fmt.Fprintf(out, "Note: %s=%s is set and takes precedence; remove it to use %s.\n", envDBDriver, settings.Pinned, dialectLabel(dst))
	}
	return nil
}
