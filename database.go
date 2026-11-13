package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// dbSettings is the database configuration resolved from the environment.
type dbSettings struct {
	Dialect    storage.Dialect
	SQLitePath string
	Postgres   postgresSettings
	// Warning explains why ytdm is not using the backend that was asked for.
	Warning string
}

// dbSettingsFromEnv resolves which backend to use. YTDM_DB_DRIVER picks it explicitly;
// when unset, providing any Postgres connection setting selects Postgres, otherwise
// SQLite. It never fails: ytdm always runs, so invalid Postgres settings (or an
// unknown driver) fall back to SQLite with a Warning.
func dbSettingsFromEnv(getenv func(string) string) dbSettings {
	s := dbSettings{
		SQLitePath: strings.TrimSpace(getenv(envSQLitePath)),
		Postgres:   postgresSettingsFromEnv(getenv),
		Dialect:    storage.DialectSQLite,
	}
	if s.SQLitePath == "" {
		s.SQLitePath = defaultSQLitePath
	}

	dialect, err := parseDialect(getenv(envDBDriver))
	if err != nil {
		s.Warning = fmt.Sprintf("%s: %v; using SQLite", envDBDriver, err)
		return s
	}
	if dialect == "" && s.Postgres.configured() {
		dialect = storage.DialectPostgres
	}
	if dialect == storage.DialectPostgres {
		if _, err := s.Postgres.dsn(); err != nil {
			s.Warning = fmt.Sprintf("PostgreSQL settings are invalid (%v); using SQLite", err)
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

// openStorage opens the configured backend. When Postgres is configured but can't be
// opened, it falls back to SQLite and records why in s.Warning, so ytdm always starts.
// Only a SQLite failure is returned as an error.
func openStorage(s *dbSettings, logf func(format string, args ...any)) (*storage.Storage, error) {
	if s.Dialect == storage.DialectPostgres {
		opts, err := s.options(storage.DialectPostgres)
		if err == nil {
			for attempt := 0; ; attempt++ {
				var store *storage.Storage
				store, err = storage.Open(opts)
				if err == nil {
					return store, nil
				}
				if attempt >= len(postgresConnectRetries) {
					break
				}
				logf("PostgreSQL not ready (%v); retrying in %s", err, postgresConnectRetries[attempt])
				time.Sleep(postgresConnectRetries[attempt])
			}
		}
		s.Warning = fmt.Sprintf("could not open %s: %v; using SQLite", s.Postgres.describe(), err)
		s.Dialect = storage.DialectSQLite
	}

	opts, err := s.options(storage.DialectSQLite)
	if err != nil {
		return nil, err
	}
	return storage.Open(opts)
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

// databaseStatus is what /api/status reports about storage.
var databaseStatus struct {
	Backend string
	Warning string
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
	overwrite := fs.Bool("overwrite", false, "replace any channels/videos already in the destination")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: ytdm migrate-db --to postgres|sqlite [flags]")
		fmt.Fprintln(out, "\nCopies every channel and video from one database backend to the other.")
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

	result, err := storage.CopyDatabase(srcStore, dstStore, *overwrite)
	if err != nil {
		return err
	}
	for _, tc := range result.Tables {
		fmt.Fprintf(out, "  %-26s %d row(s)\n", tc.Table, tc.Rows)
	}
	fmt.Fprintf(out, "Done. Set %s=%s to run ytdm against the destination.\n", envDBDriver, dst)
	return nil
}
