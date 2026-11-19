package storage

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// openSQLiteForCopyTest opens a SQLite Storage directly (never redirected to PostgreSQL
// by the test hook), so copy tests always have a real SQLite side.
func openSQLiteForCopyTest(t *testing.T, name string) *Storage {
	t.Helper()
	store, err := Open(Options{Dialect: DialectSQLite, SQLitePath: filepath.Join(t.TempDir(), name)})
	if err != nil {
		t.Fatalf("Open(sqlite) error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func seedCopyFixture(t *testing.T, s *Storage) {
	t.Helper()
	pub := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := s.AddChannel(Channel{
		ID: "UCaaa", URL: "https://www.youtube.com/channel/UCaaa", Name: "Alpha",
		RetentionDays: 9, DisablePruning: true, CutoffDate: pub, VideoFormat: "mp4",
		SkipAutoDownload: true, DisableNotifications: true, BacklogScanComplete: true, SponsorBlockMark: true,
		DownloadedVideos: []DownloadedVideo{{ID: "vid00000001", Title: "One", DownloadDate: pub, PublishDate: pub, DisablePruning: true}},
		FeedVideos:       []FeedVideo{{ID: "vid00000002", Title: "Two", URL: "https://www.youtube.com/watch?v=vid00000002", PublishedAt: pub, AddedAt: pub, ManualDownloadOnly: true}},
		PrunedVideos:     []PrunedVideo{{ID: "vid00000003", PublishDate: pub}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(Channel{ID: "UCbbb", URL: "https://www.youtube.com/@b", Name: "Beta"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddVideo(Video{
		ID: "vid00000004", URL: "https://www.youtube.com/watch?v=vid00000004", Title: "Solo",
		AddedDate: pub, RetentionDays: 3, Uploader: "Someone", UploaderID: "UCzzz", SponsorBlockMark: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkVideoAsDownloaded("vid00000004", "vid00000004", "Solo", pub); err != nil {
		t.Fatal(err)
	}
}

func assertSameData(t *testing.T, want, got *Storage) {
	t.Helper()
	if w, g := want.GetChannels(), got.GetChannels(); !reflect.DeepEqual(w, g) {
		t.Errorf("channels differ after copy:\nwant %+v\ngot  %+v", w, g)
	}
	if w, g := want.GetVideos(), got.GetVideos(); !reflect.DeepEqual(w, g) {
		t.Errorf("videos differ after copy:\nwant %+v\ngot  %+v", w, g)
	}
}

func TestCopyDatabaseSQLiteToSQLite(t *testing.T) {
	src := openSQLiteForCopyTest(t, "src.db")
	dst := openSQLiteForCopyTest(t, "dst.db")
	seedCopyFixture(t, src)

	res, err := CopyDatabase(src, dst, false)
	if err != nil {
		t.Fatalf("CopyDatabase() error = %v", err)
	}
	if len(res.Tables) != len(copyTables) {
		t.Errorf("expected %d tables in result, got %d", len(copyTables), len(res.Tables))
	}
	assertSameData(t, src, dst)

	// New rows in the destination must not collide with copied primary keys.
	if err := dst.AddChannel(Channel{ID: "UCccc", URL: "u", Name: "Gamma"}); err != nil {
		t.Fatalf("AddChannel after copy: %v", err)
	}
}

func TestCopyDatabaseRefusesNonEmptyDestination(t *testing.T) {
	src := openSQLiteForCopyTest(t, "src.db")
	dst := openSQLiteForCopyTest(t, "dst.db")
	seedCopyFixture(t, src)
	if err := dst.AddChannel(Channel{ID: "UCkeep", URL: "u", Name: "Keep"}); err != nil {
		t.Fatal(err)
	}

	if _, err := CopyDatabase(src, dst, false); err == nil {
		t.Fatal("expected an error copying into a non-empty destination")
	}
	if !dst.HasChannel("UCkeep") || dst.HasChannel("UCaaa") {
		t.Fatal("a refused copy must leave the destination untouched")
	}

	if _, err := CopyDatabase(src, dst, true); err != nil {
		t.Fatalf("CopyDatabase(overwrite) error = %v", err)
	}
	if dst.HasChannel("UCkeep") {
		t.Error("overwrite should replace existing destination rows")
	}
	assertSameData(t, src, dst)
}

// TestCopyDatabaseRoundTripPostgres moves data SQLite -> PostgreSQL -> SQLite and checks
// nothing changes on the way. Skipped unless YTDM_TEST_POSTGRES_URL is set.
func TestCopyDatabaseRoundTripPostgres(t *testing.T) {
	pg := newPostgresTestStorage(t)
	src := openSQLiteForCopyTest(t, "src.db")
	back := openSQLiteForCopyTest(t, "back.db")
	seedCopyFixture(t, src)

	if _, err := CopyDatabase(src, pg, false); err != nil {
		t.Fatalf("sqlite -> postgres: %v", err)
	}
	assertSameData(t, src, pg)

	// Identity sequences must have moved past the copied ids.
	if err := pg.AddChannel(Channel{ID: "UCnew", URL: "u", Name: "New"}); err != nil {
		t.Fatalf("AddChannel on postgres after copy: %v", err)
	}
	if err := pg.RemoveChannel("UCnew"); err != nil {
		t.Fatal(err)
	}

	if _, err := CopyDatabase(pg, back, false); err != nil {
		t.Fatalf("postgres -> sqlite: %v", err)
	}
	assertSameData(t, src, back)
}
