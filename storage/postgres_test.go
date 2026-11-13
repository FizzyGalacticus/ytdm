package storage

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// postgresTestURLEnv names a PostgreSQL server URL (e.g.
// postgres://user:pass@localhost:5432/db?sslmode=disable). When it is set, every test in
// this package that calls NewStorage runs against PostgreSQL instead of SQLite: each
// distinct database file path maps to its own schema, so tests that reopen the same path
// to check persistence see the same data, just as they would with a file.
const postgresTestURLEnv = "YTDM_TEST_POSTGRES_URL"

func TestMain(m *testing.M) {
	baseURL := os.Getenv(postgresTestURLEnv)
	if baseURL == "" {
		os.Exit(m.Run())
	}

	admin, err := sql.Open("postgres", baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening %s: %v\n", postgresTestURLEnv, err)
		os.Exit(1)
	}

	var mu sync.Mutex
	schemas := map[string]bool{}
	testOpenHook = func(filePath string) (*Storage, error) {
		sum := sha1.Sum([]byte(filePath))
		schema := "ytdm_test_" + hex.EncodeToString(sum[:8])
		mu.Lock()
		if !schemas[schema] {
			if _, err := admin.Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema); err != nil {
				mu.Unlock()
				return nil, err
			}
			schemas[schema] = true
		}
		mu.Unlock()
		dsn, err := withSearchPath(baseURL, schema)
		if err != nil {
			return nil, err
		}
		return Open(Options{
			Dialect:        DialectPostgres,
			PostgresDSN:    dsn,
			LegacyJSONPath: filepath.Join(filepath.Dir(filePath), "data.json"),
		})
	}

	code := m.Run()

	for schema := range schemas {
		admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	}
	admin.Close()
	os.Exit(code)
}

func withSearchPath(baseURL, schema string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// newPostgresTestStorage opens a fresh, empty PostgreSQL-backed Storage in its own
// schema, skipping the test when no test server is configured.
func newPostgresTestStorage(t *testing.T) *Storage {
	t.Helper()
	baseURL := os.Getenv(postgresTestURLEnv)
	if baseURL == "" {
		t.Skipf("%s not set", postgresTestURLEnv)
	}
	admin, err := sql.Open("postgres", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	sum := sha1.Sum([]byte(t.Name()))
	schema := "ytdm_pgtest_" + hex.EncodeToString(sum[:8])
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE; CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a, err := sql.Open("postgres", baseURL); err == nil {
			a.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
			a.Close()
		}
	})

	dsn, err := withSearchPath(baseURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(Options{Dialect: DialectPostgres, PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("Open(postgres) error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestPostgresConcurrentWritesStaySerialized hammers read-then-write transactions from
// many goroutines at once, as the scheduler, download workers, and API can. On SQLite
// the single connection serializes them; on Postgres the write lock must do the same,
// or racing "insert unless exists" transactions would hit unique-key errors.
func TestPostgresConcurrentWritesStaySerialized(t *testing.T) {
	store := newPostgresTestStorage(t)
	if err := store.AddChannel(Channel{ID: "UCrace", URL: "u", Name: "Race"}); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	const videos = 40
	var wg sync.WaitGroup
	errs := make(chan error, workers*videos*2)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for v := 0; v < videos; v++ {
				id := fmt.Sprintf("racevid%04d", v)
				// Everyone inserts the same pending video...
				if err := store.UpsertFeedVideo("UCrace", FeedVideo{ID: id, Title: "Race", URL: "https://www.youtube.com/watch?v=" + id}); err != nil {
					errs <- err
				}
				// ...and half of them race to mark it downloaded.
				if i%2 == 0 {
					if err := store.MarkVideoAsDownloaded("UCrace", id, "Race", time.Time{}); err != nil {
						errs <- err
					}
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	failures := 0
	for err := range errs {
		if failures++; failures <= 3 {
			t.Errorf("concurrent write failed: %v", err)
		}
	}

	ch, _ := store.GetChannel("UCrace")
	if len(ch.DownloadedVideos) != videos || len(ch.FeedVideos) != 0 {
		t.Errorf("expected %d downloaded videos, got %d downloaded / %d pending", videos, len(ch.DownloadedVideos), len(ch.FeedVideos))
	}
}
