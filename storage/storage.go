// Package storage persists ytdm's tracked channels and videos in a normalized SQLite or
// PostgreSQL database. The exported Channel/Video/DownloadedVideo/FeedVideo/PrunedVideo types below
// are not the database's native shape (which is normalized across several tables) -- they
// are the JSON-compatible view the rest of the application and the legacy data.json
// importer both expect, reassembled from the normalized schema on read.
package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// sourceYouTube is sources.id for the 'youtube' row seeded by migration 0001.
const sourceYouTube = 1

// PrunedVideo records a video that was downloaded and then pruned, along with its
// publish date so that expired entries can be evicted from the list over time.
type PrunedVideo struct {
	ID          string    `json:"id"`
	PublishDate time.Time `json:"publish_date"`
}

// FeedVideo records a video seen in the channel's RSS feed that falls within the
// retention window but has not yet been downloaded.
type FeedVideo struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	URL                string    `json:"url"`
	PublishedAt        time.Time `json:"published_at"`
	AddedAt            time.Time `json:"added_at"`
	IsShort            bool      `json:"is_short,omitempty"`
	ManualDownloadOnly bool      `json:"manual_download_only,omitempty"`
}

// DownloadedVideo tracks a downloaded video with its download date.
type DownloadedVideo struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	DownloadDate   time.Time `json:"download_date"`
	PublishDate    time.Time `json:"publish_date"`
	DisablePruning bool      `json:"disable_pruning"`
}

// Channel represents a YouTube channel to monitor.
type Channel struct {
	ID                  string            `json:"id"`
	URL                 string            `json:"url"`
	Name                string            `json:"name"`
	LastChecked         time.Time         `json:"last_checked"`
	RetentionDays       int               `json:"retention_days"`
	DisablePruning      bool              `json:"disable_pruning"`
	CutoffDate          time.Time         `json:"cutoff_date"`
	VideoQuality        string            `json:"video_quality"`
	VideoFormat         string            `json:"video_format"`
	DownloadShorts      bool              `json:"download_shorts"`
	PrunedVideos        []PrunedVideo     `json:"pruned_videos"`
	DownloadedVideos    []DownloadedVideo `json:"downloaded_videos"`
	FeedVideos          []FeedVideo       `json:"feed_videos,omitempty"`
	SkipAutoDownload    bool              `json:"skip_auto_download"`
	LastError           string            `json:"last_error,omitempty"`
	LastErrorTime       time.Time         `json:"last_error_time,omitempty"`
	ThumbnailURL        string            `json:"thumbnail_url,omitempty"`
	BacklogScanComplete bool              `json:"backlog_scan_complete,omitempty"`
	// DisableNotifications opts this channel out of new-video webhook notifications.
	DisableNotifications bool `json:"disable_notifications"`
}

// Video represents a specific YouTube video to monitor.
type Video struct {
	ID               string            `json:"id"`
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	AddedDate        time.Time         `json:"added_date"`
	LastChecked      time.Time         `json:"last_checked"`
	RetentionDays    int               `json:"retention_days"`
	DisablePruning   bool              `json:"disable_pruning"`
	VideoQuality     string            `json:"video_quality"`
	VideoFormat      string            `json:"video_format"`
	DownloadShorts   bool              `json:"download_shorts"`
	Uploader         string            `json:"uploader,omitempty"`
	UploaderID       string            `json:"uploader_id,omitempty"`
	DownloadedVideos []DownloadedVideo `json:"downloaded_videos"`
	LastError        string            `json:"last_error,omitempty"`
	LastErrorTime    time.Time         `json:"last_error_time,omitempty"`
}

// StorageData is the legacy flat-JSON shape, kept solely for the one-time import step.
type StorageData struct {
	Channels []Channel `json:"channels"`
	Videos   []Video   `json:"videos"`
}

// Storage manages persistent data in either SQLite or PostgreSQL.
type Storage struct {
	db       *dbConn
	notifyCh chan struct{}
}

// queryer is satisfied by both *dbConn and *dbTx, letting helpers run inside or
// outside a transaction.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// execer is satisfied by both *dbConn and *dbTx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Options selects and configures the database backend.
type Options struct {
	// Dialect is DialectSQLite (the default when empty) or DialectPostgres.
	Dialect Dialect
	// SQLitePath is the SQLite database file (SQLite only).
	SQLitePath string
	// PostgresDSN is a lib/pq key=value connection string or postgres:// URL. Callers
	// should spell out user, password, and dbname rather than rely on libpq defaults.
	PostgresDSN string
	// PostgresSchema, when set, is created if missing before migrations run. The DSN
	// must also select it (search_path) so ytdm's tables live there.
	PostgresSchema string
	// LegacyJSONPath is a pre-SQL data.json to import once into an empty database.
	// Empty disables the import.
	LegacyJSONPath string
}

// testOpenHook lets the storage tests run the whole suite against PostgreSQL by
// redirecting NewStorage. It is always nil outside tests.
var testOpenHook func(filePath string) (*Storage, error)

// NewStorage opens (creating if necessary) the SQLite database at filePath, applies any
// pending schema migrations, and imports a legacy data.json sitting next to it if one
// exists and hasn't been imported yet.
func NewStorage(filePath string) (*Storage, error) {
	if testOpenHook != nil {
		return testOpenHook(filePath)
	}
	return Open(Options{
		Dialect:        DialectSQLite,
		SQLitePath:     filePath,
		LegacyJSONPath: filepath.Join(filepath.Dir(filePath), "data.json"),
	})
}

// Open connects to the configured backend, applies pending schema migrations, and runs
// the one-time legacy JSON import when LegacyJSONPath is set.
func Open(opts Options) (*Storage, error) {
	conn, err := openConn(opts)
	if err != nil {
		return nil, err
	}

	if err := applyMigrations(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("applying schema migrations: %w", err)
	}

	if opts.LegacyJSONPath != "" {
		if err := importLegacyJSONIfNeeded(conn, opts.LegacyJSONPath); err != nil {
			conn.Close()
			return nil, fmt.Errorf("importing legacy data: %w", err)
		}
	}

	return &Storage{db: conn, notifyCh: make(chan struct{}, 16)}, nil
}

func openConn(opts Options) (*dbConn, error) {
	switch opts.Dialect {
	case DialectPostgres:
		return openPostgres(opts.PostgresDSN, opts.PostgresSchema)
	case DialectSQLite, "":
		return openSQLite(opts.SQLitePath)
	default:
		return nil, fmt.Errorf("unsupported database dialect %q", opts.Dialect)
	}
}

func openSQLite(filePath string) (*dbConn, error) {
	if filePath == "" {
		return nil, fmt.Errorf("sqlite database path is empty")
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)",
		filePath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Defensive: re-assert the pragmas that matter even if the DSN mechanism didn't take
	// effect as expected. foreign_keys in particular is a per-connection SQLite setting.
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("applying %q: %w", pragma, err)
		}
	}
	return newDBConn(db, DialectSQLite), nil
}

func openPostgres(dsn, schema string) (*dbConn, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening postgres connection: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if schema != "" {
		if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS ` + quoteIdent(schema)); err != nil {
			db.Close()
			return nil, fmt.Errorf("creating schema %s: %w", schema, err)
		}
	}
	return newDBConn(db, DialectPostgres), nil
}

// quoteIdent quotes a PostgreSQL identifier.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Dialect reports which backend this Storage uses.
func (s *Storage) Dialect() Dialect {
	return s.db.Dialect()
}

// Close releases the underlying database connection.
func (s *Storage) Close() error {
	return s.db.Close()
}

// notify signals that storage data has changed (non-blocking).
func (s *Storage) notify() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

// NotifyCh returns a channel that receives a value whenever storage data changes.
func (s *Storage) NotifyCh() <-chan struct{} {
	return s.notifyCh
}

// timeToNull converts t to a value suitable for a SQL TEXT column, encoding Go's zero
// time.Time{} as SQL NULL so that code relying on time.Time.IsZero() as an "unset"
// sentinel continues to work unchanged after a round trip through SQLite.
func timeToNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// nullToTime is the inverse of timeToNull.
func nullToTime(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// resolveChannelPK looks up the internal synthetic PK for a channel given its
// YouTube-native canonical channel ID (e.g. "UC...").
func resolveChannelPK(q queryer, sourceChannelID string) (int64, bool, error) {
	var id int64
	err := q.QueryRow(
		`SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?`,
		sourceYouTube, sourceChannelID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// resolveVideoPK looks up the internal synthetic PK for a video given its YouTube-native
// video ID.
func resolveVideoPK(q queryer, sourceVideoID string) (int64, bool, error) {
	var id int64
	err := q.QueryRow(
		`SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?`,
		sourceYouTube, sourceVideoID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
