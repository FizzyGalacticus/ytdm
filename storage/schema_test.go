package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyMigrationsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	if err := applyMigrations(db); err != nil {
		t.Fatalf("first applyMigrations() error = %v", err)
	}

	var firstCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&firstCount); err != nil {
		t.Fatalf("querying schema_migrations: %v", err)
	}
	if firstCount == 0 {
		t.Fatal("expected at least one recorded migration after first apply")
	}

	// Re-running must be a safe no-op: no error, and it must not attempt to
	// re-CREATE TABLE (which would fail against the real driver).
	if err := applyMigrations(db); err != nil {
		t.Fatalf("second applyMigrations() error = %v", err)
	}

	var secondCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&secondCount); err != nil {
		t.Fatalf("querying schema_migrations: %v", err)
	}
	if secondCount != firstCount {
		t.Fatalf("expected migration count to stay %d after re-apply, got %d", firstCount, secondCount)
	}
}

// TestReopenPreservesChildRowsAcrossRestart exercises the FK/cascade wiring across a
// fresh NewStorage call on the same file, including DownloadedVideo/FeedVideo/PrunedVideo
// child rows -- not just the top-level channel/video row that TestStoragePersistence
// already covers.
func TestReopenPreservesChildRowsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	first, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	publishA := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	publishB := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	publishC := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)

	channel := Channel{
		ID:   "UCreopen",
		Name: "Reopen Channel",
		URL:  "https://youtube.com/@reopen",
		DownloadedVideos: []DownloadedVideo{
			{ID: "dv-1", Title: "Downloaded", PublishDate: publishA, DownloadDate: publishA},
		},
		FeedVideos: []FeedVideo{
			{ID: "fv-1", Title: "Pending", URL: "https://youtube.com/watch?v=fv-1", PublishedAt: publishB},
		},
		PrunedVideos: []PrunedVideo{
			{ID: "pv-1", PublishDate: publishC},
		},
	}
	if err := first.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	video := Video{
		ID:    "standalone-1",
		Title: "Standalone",
		URL:   "https://youtube.com/watch?v=standalone-1",
	}
	if err := first.AddVideo(video); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	second, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("reopen NewStorage() error = %v", err)
	}

	channels := second.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel after reopen, got %d", len(channels))
	}
	ch := channels[0]
	if len(ch.DownloadedVideos) != 1 || ch.DownloadedVideos[0].ID != "dv-1" {
		t.Errorf("DownloadedVideos not preserved across reopen: %+v", ch.DownloadedVideos)
	}
	if len(ch.FeedVideos) != 1 || ch.FeedVideos[0].ID != "fv-1" {
		t.Errorf("FeedVideos not preserved across reopen: %+v", ch.FeedVideos)
	}
	if len(ch.PrunedVideos) != 1 || ch.PrunedVideos[0].ID != "pv-1" {
		t.Errorf("PrunedVideos not preserved across reopen: %+v", ch.PrunedVideos)
	}

	videos := second.GetVideos()
	if len(videos) != 1 || videos[0].ID != "standalone-1" {
		t.Errorf("standalone video not preserved across reopen: %+v", videos)
	}

	// Removing the channel after reopen must cascade its child rows too.
	if err := second.RemoveChannel("UCreopen"); err != nil {
		t.Fatalf("RemoveChannel() error = %v", err)
	}
	var remainingChildRows int
	for _, table := range []string{"channel_sources", "video_sources", "channel_videos"} {
		var count int
		if err := second.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if table == "video_sources" {
			// The standalone video's own video_sources row should remain untouched.
			if count != 1 {
				t.Errorf("expected video_sources to retain the standalone video's row, got %d rows", count)
			}
			continue
		}
		remainingChildRows += count
	}
	if remainingChildRows != 0 {
		t.Errorf("expected channel cascade to clear channel_sources/channel_videos, %d rows remain", remainingChildRows)
	}
}
