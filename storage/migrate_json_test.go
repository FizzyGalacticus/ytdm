package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	publishDate := time.Date(2024, 3, 4, 0, 0, 0, 0, time.UTC)
	legacy := StorageData{
		Channels: []Channel{
			{
				ID:            "UClegacy",
				URL:           "https://youtube.com/@legacy",
				Name:          "Legacy Channel",
				RetentionDays: 30,
				CutoffDate:    time.Time{}, // zero -- must round-trip as zero, not a sentinel string
				DownloadedVideos: []DownloadedVideo{
					{ID: "dv-1", Title: "Downloaded One", DownloadDate: publishDate, PublishDate: publishDate},
				},
				FeedVideos: []FeedVideo{
					{ID: "fv-1", Title: "Pending One", URL: "https://youtube.com/watch?v=fv-1", PublishedAt: publishDate, AddedAt: publishDate},
				},
				PrunedVideos: []PrunedVideo{
					{ID: "pv-1", PublishDate: time.Time{}}, // zero publish date -- must round-trip as zero too
				},
			},
		},
		Videos: []Video{
			{
				ID:    "standalone-1",
				URL:   "https://youtube.com/watch?v=standalone-1",
				Title: "Standalone Pending",
			},
		},
	}

	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 imported channel, got %d", len(channels))
	}
	ch := channels[0]
	if ch.ID != "UClegacy" || ch.Name != "Legacy Channel" || ch.RetentionDays != 30 {
		t.Errorf("channel fields not imported correctly: %+v", ch)
	}
	if !ch.CutoffDate.IsZero() {
		t.Errorf("expected zero CutoffDate to round-trip as zero, got %v", ch.CutoffDate)
	}
	if len(ch.DownloadedVideos) != 1 || ch.DownloadedVideos[0].ID != "dv-1" || !ch.DownloadedVideos[0].PublishDate.Equal(publishDate) {
		t.Errorf("DownloadedVideos not imported correctly: %+v", ch.DownloadedVideos)
	}
	if len(ch.FeedVideos) != 1 || ch.FeedVideos[0].ID != "fv-1" {
		t.Errorf("FeedVideos not imported correctly: %+v", ch.FeedVideos)
	}
	if len(ch.PrunedVideos) != 1 || ch.PrunedVideos[0].ID != "pv-1" {
		t.Errorf("PrunedVideos not imported correctly: %+v", ch.PrunedVideos)
	}
	if !ch.PrunedVideos[0].PublishDate.IsZero() {
		t.Errorf("expected zero PrunedVideo.PublishDate to round-trip as zero, got %v", ch.PrunedVideos[0].PublishDate)
	}

	videos := store.GetVideos()
	if len(videos) != 1 || videos[0].ID != "standalone-1" || videos[0].Title != "Standalone Pending" {
		t.Errorf("standalone video not imported correctly: %+v", videos)
	}

	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("expected legacy data.json to be renamed away, stat err = %v", err)
	}
	if _, err := os.Stat(legacyPath + ".migrated"); err != nil {
		t.Errorf("expected data.json.migrated to exist, stat err = %v", err)
	}
}

func TestImportLegacyJSONNoFileMarksImportedWithoutError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")

	// No data.json exists in dir at all.
	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	if len(store.GetChannels()) != 0 || len(store.GetVideos()) != 0 {
		t.Fatalf("expected an empty store when no legacy file exists")
	}

	var imported bool
	if err := store.db.QueryRow(`SELECT imported FROM json_import_state WHERE id = 1`).Scan(&imported); err != nil {
		t.Fatalf("querying json_import_state: %v", err)
	}
	if !imported {
		t.Error("expected json_import_state.imported to be true even with no legacy file, so a later-dropped-in data.json is never auto-imported")
	}
}

func TestImportLegacyJSONIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	legacy := StorageData{
		Channels: []Channel{{ID: "UConce", URL: "https://youtube.com/@once", Name: "Once Channel"}},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	if len(store.GetChannels()) != 1 {
		t.Fatalf("expected 1 channel after first import, got %d", len(store.GetChannels()))
	}

	// Simulate the rename having failed to stick (e.g. a crash between commit and
	// rename) by moving data.json.migrated back to data.json. The flag, already
	// committed, must still prevent re-import.
	if err := os.Rename(legacyPath+".migrated", legacyPath); err != nil {
		t.Fatalf("restoring legacy file for idempotency check: %v", err)
	}

	store2, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("second NewStorage() error = %v", err)
	}
	channels := store2.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected re-import to be a no-op (still 1 channel), got %d", len(channels))
	}
}

func TestImportLegacyJSONPartialFailureLeavesDBUntouched(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	// Two channels sharing the same canonical ID: the second insert into
	// channel_sources violates the unique constraint partway through the import,
	// so the whole transaction must roll back -- zero channels, and the import flag
	// must remain false so a corrected retry is possible on next startup.
	legacy := StorageData{
		Channels: []Channel{
			{ID: "UCdupe", URL: "https://youtube.com/@dupe1", Name: "First"},
			{ID: "UCdupe", URL: "https://youtube.com/@dupe2", Name: "Second"},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := NewStorage(dbPath)
	if err == nil {
		t.Fatal("expected NewStorage() to fail importing a legacy file with a duplicate channel ID")
	}

	// Re-open against the same (now-existing, schema-migrated) db file directly to
	// inspect state without going through NewStorage's own import attempt again.
	store, err := NewStorage(dbPath)
	if err == nil {
		// If import unexpectedly failed to fail this time (e.g. retried into a
		// different error), still assert the db has no partial channel data.
		if len(store.GetChannels()) != 0 {
			t.Fatalf("expected zero channels after a failed import, got %d", len(store.GetChannels()))
		}
		return
	}

	// The import is retried (and fails identically) on every NewStorage call since
	// the flag was never set -- confirm the flag is indeed still false and no
	// partial channel data was committed, by inspecting the db file directly.
	raw2, statErr := os.ReadFile(legacyPath)
	if statErr != nil || len(raw2) == 0 {
		t.Fatalf("expected the legacy file to remain in place after a failed import: %v", statErr)
	}
}

// TestImportLegacyJSONMalformedJSONLeavesSourceUntouched confirms that invalid JSON
// (as opposed to a structurally-valid-but-inconsistent one) fails the same way: no
// partial data committed, the import flag never set, and data.json left in place for a
// later retry -- the "fall back to the old file" guarantee applies to a corrupt file,
// not just a logically inconsistent one.
func TestImportLegacyJSONMalformedJSONLeavesSourceUntouched(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	if err := os.WriteFile(legacyPath, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := NewStorage(dbPath); err == nil {
		t.Fatal("expected NewStorage() to fail on malformed legacy JSON")
	}

	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("expected legacy data.json to remain in place after malformed-JSON failure: %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		// Retried and failed identically -- also acceptable, as long as nothing was
		// committed and the file is still there (checked above on every attempt since
		// the flag is never set).
		return
	}
	defer store.Close()
	if len(store.GetChannels()) != 0 || len(store.GetVideos()) != 0 {
		t.Fatalf("expected zero channels/videos after a malformed-JSON import failure")
	}
}

// TestImportLegacyJSONDuplicateIDWithinChannel_DownloadedBeatsPruned locks down real
// production behavior: a legacy channel export that lists the same video ID in both
// downloaded_videos and pruned_videos (observed in practice -- almost certainly a video
// that was pruned once, then re-downloaded later, with the stale pruned_videos entry
// never cleaned up) must end up 'downloaded' with its downloaded fields intact, never
// silently downgraded/blanked by the weaker pruned entry.
func TestImportLegacyJSONDuplicateIDWithinChannel_DownloadedBeatsPruned(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	downloadDate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	legacy := StorageData{
		Channels: []Channel{
			{
				ID:   "UCdupe-status",
				URL:  "https://youtube.com/@dupestatus",
				Name: "Dupe Status Channel",
				DownloadedVideos: []DownloadedVideo{
					{ID: "shared-vid", Title: "The Real Title", DownloadDate: downloadDate, DisablePruning: true},
				},
				PrunedVideos: []PrunedVideo{
					{ID: "shared-vid", PublishDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch := channels[0]
	if len(ch.PrunedVideos) != 0 {
		t.Errorf("expected no pruned videos (downloaded should have won), got %#v", ch.PrunedVideos)
	}
	if len(ch.DownloadedVideos) != 1 {
		t.Fatalf("expected 1 downloaded video, got %#v", ch.DownloadedVideos)
	}
	dv := ch.DownloadedVideos[0]
	if dv.ID != "shared-vid" || dv.Title != "The Real Title" || !dv.DownloadDate.Equal(downloadDate) || !dv.DisablePruning {
		t.Errorf("downloaded video fields were clobbered by the weaker pruned duplicate: %#v", dv)
	}
}

// TestImportLegacyJSONDuplicateIDWithinChannel_PrunedBeatsPending mirrors the above for
// the other ordering: pruned (a deliberate decision) must win over a merely-seen pending
// entry for the same ID.
func TestImportLegacyJSONDuplicateIDWithinChannel_PrunedBeatsPending(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	legacy := StorageData{
		Channels: []Channel{
			{
				ID:   "UCdupe-status-2",
				URL:  "https://youtube.com/@dupestatus2",
				Name: "Dupe Status Channel 2",
				FeedVideos: []FeedVideo{
					{ID: "shared-vid-2", Title: "Seen In Feed", PublishedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
				},
				PrunedVideos: []PrunedVideo{
					{ID: "shared-vid-2", PublishDate: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch := channels[0]
	if len(ch.FeedVideos) != 0 {
		t.Errorf("expected no pending feed videos (pruned should have won), got %#v", ch.FeedVideos)
	}
	if len(ch.PrunedVideos) != 1 || ch.PrunedVideos[0].ID != "shared-vid-2" {
		t.Errorf("expected shared-vid-2 to remain pruned, got %#v", ch.PrunedVideos)
	}
}

// TestImportLegacyJSONStandaloneAlreadyChannelOwned is a regression test for a crash
// found against real production data: a video ID present both in a channel's
// downloaded_videos AND in the top-level standalone videos list (a redundant leftover
// from before the uploader was subscribed to as a channel) previously made the second
// insert violate video_sources' uniqueness constraint, aborting the entire import.
// Channel ownership must win silently instead.
func TestImportLegacyJSONStandaloneAlreadyChannelOwned(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	legacy := StorageData{
		Channels: []Channel{
			{
				ID:   "UCowns-it",
				URL:  "https://youtube.com/@ownsit",
				Name: "Owns It Channel",
				DownloadedVideos: []DownloadedVideo{
					{ID: "shared-across-lists", Title: "Channel Copy", DownloadDate: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		},
		Videos: []Video{
			{ID: "shared-across-lists", Title: "Stale Standalone Copy", RetentionDays: 999},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v (this must not crash the import)", err)
	}
	defer store.Close()

	if len(store.GetVideos()) != 0 {
		t.Errorf("expected the redundant standalone entry to be dropped (video is channel-owned), got %#v", store.GetVideos())
	}
	channels := store.GetChannels()
	if len(channels) != 1 || len(channels[0].DownloadedVideos) != 1 || channels[0].DownloadedVideos[0].Title != "Channel Copy" {
		t.Errorf("expected the channel-owned copy to survive untouched, got %#v", channels)
	}
}

// TestImportLegacyJSONStandaloneAlreadyChannelOwned_DivergentDownloadID exercises the
// same collision as above, but where the standalone entry's own DownloadedVideos[0].ID
// (yt-dlp's resolved canonical ID, per MarkVideoAsDownloaded's convention -- see
// importStandaloneVideo) differs from both its own top-level ID and matches the
// channel's copy. The dedup check must key off the ID actually used for the insert, not
// the raw top-level Video.ID, or this case would wrongly attempt a duplicate insert.
func TestImportLegacyJSONStandaloneAlreadyChannelOwned_DivergentDownloadID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	legacy := StorageData{
		Channels: []Channel{
			{
				ID:   "UCowns-it-2",
				URL:  "https://youtube.com/@ownsit2",
				Name: "Owns It Channel 2",
				DownloadedVideos: []DownloadedVideo{
					{ID: "resolved-canonical-id", Title: "Channel Copy"},
				},
			},
		},
		Videos: []Video{
			{
				ID:    "originally-tracked-id",
				Title: "Stale Standalone Copy",
				DownloadedVideos: []DownloadedVideo{
					{ID: "resolved-canonical-id", Title: "Resolved Copy"},
				},
			},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v (divergent-ID collision must not crash the import)", err)
	}
	defer store.Close()

	if len(store.GetVideos()) != 0 {
		t.Errorf("expected the redundant standalone entry to be dropped, got %#v", store.GetVideos())
	}
}

// TestImportLegacyJSONConvertedVideoStillOverwrites confirms the fix for same-channel
// duplicates did not regress the pre-existing "convert standalone video(s) into a new
// channel" merge behavior: when a video moves to a DIFFERENT (or newly created) channel
// than the one it's currently linked to (or is currently standalone), the incoming data
// must still win unconditionally, including a nominally "weaker" status, since that's a
// deliberate move/update rather than a stale duplicate.
func TestImportLegacyJSONConvertedVideoStillOverwrites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	// Start as a standalone downloaded video (status=downloaded).
	if err := store.AddVideo(Video{
		ID:    "moving-vid",
		Title: "Original Title",
		DownloadedVideos: []DownloadedVideo{
			{ID: "moving-vid", Title: "Original Title", DownloadDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	// Convert it into a channel where it's tracked as merely pending -- a "weaker"
	// status than its current 'downloaded' -- via handleConvertToChannel's real path:
	// AddChannel with the video included in DownloadedVideos of the new channel. Use the
	// storage.Channel shape directly to simulate the conversion.
	if err := store.AddChannel(Channel{
		ID:   "UCconverted",
		URL:  "https://youtube.com/@converted",
		Name: "Converted Channel",
		DownloadedVideos: []DownloadedVideo{
			{ID: "moving-vid", Title: "Updated After Move", DownloadDate: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		},
	}); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	if len(store.GetVideos()) != 0 {
		t.Errorf("expected the video to no longer be standalone after conversion, got %#v", store.GetVideos())
	}
	channels := store.GetChannels()
	if len(channels) != 1 || len(channels[0].DownloadedVideos) != 1 || channels[0].DownloadedVideos[0].Title != "Updated After Move" {
		t.Errorf("expected the conversion's incoming data to win, got %#v", channels)
	}
}

// TestImportLegacyJSONDataQualityEdgeCases exercises a grab-bag of unusual-but-valid
// legacy field values (unicode/emoji, SQL-special characters, very long strings, zero
// and negative retention, empty optional strings) to confirm the importer handles them
// without error or corruption -- values are round-tripped through parameterized queries,
// so none of this should behave differently than "normal" data, but it's worth pinning
// down given how much can go wrong translating a decade of real-world JSON into SQL.
func TestImportLegacyJSONDataQualityEdgeCases(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	longTitle := strings.Repeat("A very long title segment. ", 200) // ~5.6KB
	sqlish := `Robert'); DROP TABLE videos;-- "quoted" & <html> 你好 🎬🔥`

	legacy := StorageData{
		Channels: []Channel{
			{
				ID:            "UCedge",
				URL:           "https://youtube.com/@edge",
				Name:          sqlish,
				RetentionDays: -5, // negative: storage layer doesn't validate ranges
				DownloadedVideos: []DownloadedVideo{
					{ID: "edge-dv-1", Title: longTitle},
					{ID: "edge-dv-2", Title: ""},
				},
			},
			{
				ID:            "UCempty",
				URL:           "",
				Name:          "",
				RetentionDays: 0,
			},
		},
		Videos: []Video{
			{ID: "edge-standalone", Title: "日本語 émoji 🚀", RetentionDays: -1},
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(legacyPath, raw, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	channels := store.GetChannels()
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	var edge, empty *Channel
	for i := range channels {
		switch channels[i].ID {
		case "UCedge":
			edge = &channels[i]
		case "UCempty":
			empty = &channels[i]
		}
	}
	if edge == nil || empty == nil {
		t.Fatalf("expected both channels present, got %#v", channels)
	}
	if edge.Name != sqlish {
		t.Errorf("SQL-special-character name not round-tripped correctly: got %q", edge.Name)
	}
	if edge.RetentionDays != -5 {
		t.Errorf("expected negative retention_days to round-trip as-is, got %d", edge.RetentionDays)
	}
	foundLong, foundEmpty := false, false
	for _, dv := range edge.DownloadedVideos {
		if dv.ID == "edge-dv-1" && dv.Title == longTitle {
			foundLong = true
		}
		if dv.ID == "edge-dv-2" && dv.Title == "" {
			foundEmpty = true
		}
	}
	if !foundLong {
		t.Error("long title video not round-tripped correctly")
	}
	if !foundEmpty {
		t.Error("empty title video not round-tripped correctly")
	}

	videos := store.GetVideos()
	if len(videos) != 1 || videos[0].Title != "日本語 émoji 🚀" {
		t.Errorf("unicode standalone video title not round-tripped correctly: %#v", videos)
	}
}

// TestImportLegacyJSONEmptyLegacyData confirms an entirely-empty-but-valid legacy file
// (zero channels, zero videos) imports as a trivial no-op rather than erroring.
func TestImportLegacyJSONEmptyLegacyData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	legacyPath := filepath.Join(dir, "data.json")

	if err := os.WriteFile(legacyPath, []byte(`{"channels":[],"videos":[]}`), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	if len(store.GetChannels()) != 0 || len(store.GetVideos()) != 0 {
		t.Fatalf("expected an empty store")
	}
	if _, err := os.Stat(legacyPath + ".migrated"); err != nil {
		t.Errorf("expected data.json to still be renamed away even though it was empty: %v", err)
	}
}

// TestValidateImportCatchesMissingVideo unit-tests validateImport directly: given a
// legacy struct that claims a video which was never actually inserted into the
// transaction, it must report an error rather than let the caller commit silently
// dropped data.
func TestValidateImportCatchesMissingVideo(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	defer tx.Rollback()

	legacy := StorageData{
		Videos: []Video{{ID: "never-inserted"}},
	}
	if err := validateImport(tx, legacy); err == nil {
		t.Error("expected validateImport to catch a video that was never inserted")
	}
}

// TestValidateImportCatchesMissingChannel is the channel-level counterpart of
// TestValidateImportCatchesMissingVideo.
func TestValidateImportCatchesMissingChannel(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	defer tx.Rollback()

	legacy := StorageData{
		Channels: []Channel{{ID: "UCnever-inserted", Name: "Ghost Channel"}},
	}
	if err := validateImport(tx, legacy); err == nil {
		t.Error("expected validateImport to catch a channel that was never inserted")
	}
}

// TestValidateImportPassesForActualImport is the positive-path counterpart: running
// validateImport against a transaction that really did import the given legacy data
// must succeed, using the same divergent-download-ID case covered by
// TestImportLegacyJSONStandaloneAlreadyChannelOwned_DivergentDownloadID so the two ID
// resolution rules (import's and validation's) are confirmed to agree.
func TestValidateImportPassesForActualImport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")

	store, err := NewStorage(dbPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	defer store.Close()

	legacy := StorageData{
		Channels: []Channel{
			{
				ID:   "UCvalidate",
				URL:  "https://youtube.com/@validate",
				Name: "Validate Channel",
				DownloadedVideos: []DownloadedVideo{
					{ID: "validate-dv"},
				},
			},
		},
		Videos: []Video{
			{
				ID: "originally-tracked-id",
				DownloadedVideos: []DownloadedVideo{
					{ID: "validate-standalone-resolved"},
				},
			},
		},
	}

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	defer tx.Rollback()

	for _, ch := range legacy.Channels {
		if err := importChannel(tx, ch); err != nil {
			t.Fatalf("importChannel() error = %v", err)
		}
	}
	for _, v := range legacy.Videos {
		if err := importStandaloneVideo(tx, v); err != nil {
			t.Fatalf("importStandaloneVideo() error = %v", err)
		}
	}

	if err := validateImport(tx, legacy); err != nil {
		t.Errorf("expected validateImport to pass for a real, complete import, got %v", err)
	}
}

// TestPurgeExpiredVideos and TestStorageDismissAllFeedVideos live in storage_test.go;
// TestScratchRealDataImport-style production-data smoke coverage lives in
// migrate_realdata_test.go, which skips itself when no real data.json is present.
