package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// realProdDataJSONPath returns the path to the actual production data.json a developer
// may have dropped next to the repo for local testing (data/ is gitignored -- this file
// never gets committed and this test never prints its content, only structural counts).
// Override with YTDM_PROD_DATA_JSON if it lives somewhere else.
func realProdDataJSONPath() string {
	if p := os.Getenv("YTDM_PROD_DATA_JSON"); p != "" {
		return p
	}
	// storage/ package tests run with the package directory as their working directory,
	// so ../data/data.json resolves to <repo>/data/data.json -- the exact path the app
	// itself would look for a legacy file at, next to data/data.db.
	return filepath.Join("..", "data", "data.json")
}

// TestRealProductionDataImports is a smoke test against a real, full-size production
// data.json (if one is present locally -- it's skipped everywhere else, including CI,
// since data/ is gitignored). It exists to catch exactly the class of bug that unit
// tests built from small hand-written fixtures miss: real data is bigger, messier, and
// has accumulated years of edge cases a synthetic fixture wouldn't think to include.
//
// Never logs or asserts on video/channel titles or names -- only structural counts and
// ID resolution -- so it's safe to run against genuinely private data.
func TestRealProductionDataImports(t *testing.T) {
	src := realProdDataJSONPath()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("no real production data.json found at %s (set YTDM_PROD_DATA_JSON to point at one): %v", src, err)
	}

	var legacy StorageData
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatalf("the real data.json at %s is not valid JSON: %v", src, err)
	}
	if len(legacy.Channels) == 0 && len(legacy.Videos) == 0 {
		t.Skip("real data.json is empty, nothing meaningful to test")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.json"), raw, 0644); err != nil {
		t.Fatalf("staging copy: %v", err)
	}

	store, err := NewStorage(filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() failed importing real production data (this must never happen): %v", err)
	}
	defer store.Close()

	channels := store.GetChannels()
	videos := store.GetVideos()
	t.Logf("imported %d channel(s), %d standalone video(s) from real data", len(channels), len(videos))

	// Every legacy channel ID must resolve to an imported channel.
	imported := make(map[string]bool, len(channels))
	for _, ch := range channels {
		imported[ch.ID] = true
	}
	for _, ch := range legacy.Channels {
		if ch.ID != "" && !imported[ch.ID] {
			t.Errorf("legacy channel %s is missing after import", ch.ID)
		}
	}

	// Every legacy video ID (across every list, using the same ID-resolution rule the
	// importer itself applies) must resolve to some video row -- proving nothing was
	// silently dropped during the merge/dedup logic that real data exercises far more
	// thoroughly than any hand-written fixture.
	var missing int
	checkID := func(id string) {
		if id == "" {
			return
		}
		if _, ok, err := resolveVideoPK(store.db, id); err != nil {
			t.Fatalf("resolveVideoPK(%s): %v", id, err)
		} else if !ok {
			missing++
		}
	}
	for _, ch := range legacy.Channels {
		for _, dv := range ch.DownloadedVideos {
			checkID(dv.ID)
		}
		for _, fv := range ch.FeedVideos {
			checkID(fv.ID)
		}
		for _, pv := range ch.PrunedVideos {
			checkID(pv.ID)
		}
	}
	for _, v := range legacy.Videos {
		checkID(standaloneVideoFinalID(v))
	}
	if missing > 0 {
		t.Errorf("%d legacy video ID(s) did not resolve to any row after import", missing)
	}

	// No dangling foreign keys.
	fkRows, err := store.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer fkRows.Close()
	if fkRows.Next() {
		t.Error("foreign key inconsistency found after importing real data")
	}

	// Re-opening must be idempotent: no duplication, no re-import, same counts.
	store2, err := NewStorage(filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatalf("second NewStorage() error = %v", err)
	}
	defer store2.Close()
	if len(store2.GetChannels()) != len(channels) || len(store2.GetVideos()) != len(videos) {
		t.Errorf("re-opening the db changed counts: channels %d->%d, videos %d->%d",
			len(channels), len(store2.GetChannels()), len(videos), len(store2.GetVideos()))
	}
}
