package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func channelByID(t *testing.T, s *Storage, id string) Channel {
	t.Helper()
	ch, ok := s.GetChannel(id)
	if !ok {
		t.Fatalf("channel %s missing", id)
	}
	return ch
}

// seedMergeTarget gives the destination its own data, overlapping the copy fixture:
// UCaaa exists with different settings and further-along video states, plus a
// destination-only channel and video.
func seedMergeTarget(t *testing.T, s *Storage) {
	t.Helper()
	pub := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := s.AddChannel(Channel{ID: "UCkeep", URL: "https://www.youtube.com/@keep", Name: "Keep",
		DownloadedVideos: []DownloadedVideo{{ID: "keepvid0001", Title: "Kept", DownloadDate: pub, PublishDate: pub}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(Channel{ID: "UCaaa", URL: "old-url", Name: "Alpha (old name)", RetentionDays: 1,
		// vid00000002 is pending in the source but already pruned here: it must stay pruned.
		PrunedVideos: []PrunedVideo{{ID: "vid00000002", PublishDate: pub}},
	}); err != nil {
		t.Fatal(err)
	}
	// vid00000004 is individually tracked in the source; here it belongs to UCkeep.
	if err := s.MergeChannelPendingVideos("UCkeep", []FeedVideo{{ID: "vid00000004", Title: "Solo (as channel video)", PublishedAt: pub}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddVideo(Video{ID: "onlyhere001", URL: "https://www.youtube.com/watch?v=onlyhere001", Title: "Only here"}); err != nil {
		t.Fatal(err)
	}
}

func assertMergedFixture(t *testing.T, src, dst *Storage) {
	t.Helper()
	alpha := channelByID(t, dst, "UCaaa")
	want := channelByID(t, src, "UCaaa")
	if alpha.Name != want.Name || alpha.URL != want.URL || alpha.RetentionDays != want.RetentionDays || !alpha.DisableNotifications {
		t.Errorf("source settings should win for UCaaa: got %+v", alpha)
	}
	if len(alpha.FeedVideos) != 0 {
		t.Errorf("vid00000002 must stay pruned, not regress to pending: feed=%+v", alpha.FeedVideos)
	}
	prunedIDs := map[string]bool{}
	for _, p := range alpha.PrunedVideos {
		prunedIDs[p.ID] = true
	}
	if !prunedIDs["vid00000002"] || !prunedIDs["vid00000003"] {
		t.Errorf("expected vid00000002 and vid00000003 pruned, got %+v", alpha.PrunedVideos)
	}
	if len(alpha.DownloadedVideos) != 1 || alpha.DownloadedVideos[0].ID != "vid00000001" {
		t.Errorf("downloaded videos = %+v", alpha.DownloadedVideos)
	}

	keep := channelByID(t, dst, "UCkeep")
	if len(keep.DownloadedVideos) != 1 || len(keep.FeedVideos) != 0 {
		t.Errorf("destination-only channel should keep its video and lose vid00000004 to the source's tracking: %+v", keep)
	}
	if !dst.HasChannel("UCbbb") {
		t.Error("source-only channel missing")
	}
	if !dst.HasVideo("onlyhere001") {
		t.Error("destination-only video missing")
	}
	solo, ok := dst.GetVideo("vid00000004")
	if !ok || solo.Title != "Solo" || len(solo.DownloadedVideos) != 1 {
		t.Errorf("vid00000004 should be individually tracked and downloaded as in the source: %+v (%v)", solo, ok)
	}
}

func TestMergeDatabaseIntoNonEmptyDestination(t *testing.T) {
	src := openSQLiteForCopyTest(t, "src.db")
	dst := openSQLiteForCopyTest(t, "dst.db")
	seedCopyFixture(t, src)
	seedMergeTarget(t, dst)

	res, err := MergeDatabase(src, dst)
	if err != nil {
		t.Fatalf("MergeDatabase() error = %v", err)
	}
	assertMergedFixture(t, src, dst)

	// Merging again changes nothing and fails nothing.
	before := dst.GetChannels()
	if _, err := MergeDatabase(src, dst); err != nil {
		t.Fatalf("second MergeDatabase() error = %v", err)
	}
	if len(dst.GetChannels()) != len(before) {
		t.Error("a repeated merge must be idempotent")
	}
	var inserted, updated int64
	for _, tc := range res.Tables {
		if tc.Table == "channels" {
			inserted, updated = tc.Inserted, tc.Updated
		}
	}
	if inserted != 1 || updated != 1 {
		t.Errorf("channels inserted/updated = %d/%d, want 1/1", inserted, updated)
	}
	// New rows still get fresh keys after a merge.
	if err := dst.AddChannel(Channel{ID: "UCafter", URL: "u", Name: "After"}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateToSwitchesLiveStorage(t *testing.T) {
	prev := oldBackendGrace
	oldBackendGrace = time.Millisecond
	t.Cleanup(func() { oldBackendGrace = prev })

	live := openSQLiteForCopyTest(t, "live.db")
	seedCopyFixture(t, live)
	targetPath := filepath.Join(t.TempDir(), "target.db")
	seed, err := Open(Options{Dialect: DialectSQLite, SQLitePath: targetPath})
	if err != nil {
		t.Fatal(err)
	}
	seedMergeTarget(t, seed)
	seed.Close()

	if _, err := live.MigrateTo(Options{Dialect: DialectSQLite, SQLitePath: targetPath}, CopyMerge, nil); err != nil {
		t.Fatalf("MigrateTo() error = %v", err)
	}
	// The same *Storage now reads and writes the target.
	if !live.HasChannel("UCkeep") {
		t.Fatal("live storage should now see the target's data")
	}
	if err := live.AddChannel(Channel{ID: "UCnewlive", URL: "u", Name: "New"}); err != nil {
		t.Fatalf("write after switch: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // old backend closes after the grace period
	if !live.HasChannel("UCnewlive") {
		t.Error("write after switch not visible")
	}

	reopened, err := Open(Options{Dialect: DialectSQLite, SQLitePath: targetPath})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.HasChannel("UCnewlive") || !reopened.HasChannel("UCaaa") {
		t.Error("writes after the switch must land in the target database")
	}
}

func TestMigrateToFailureLeavesStorageUntouched(t *testing.T) {
	live := openSQLiteForCopyTest(t, "live.db")
	seedCopyFixture(t, live)
	if _, err := live.MigrateTo(Options{Dialect: DialectSQLite, SQLitePath: filepath.Join(t.TempDir(), "missing-dir", "x.db")}, CopyMerge, nil); err == nil {
		t.Fatal("expected an error for an unopenable target")
	}
	if live.Dialect() != DialectSQLite || !live.HasChannel("UCaaa") {
		t.Error("a failed migration must leave the live storage as it was")
	}
	if err := live.AddChannel(Channel{ID: "UCstill", URL: "u", Name: "Still"}); err != nil {
		t.Errorf("writes must still work after a failed migration: %v", err)
	}
}

// TestMergeBothWaysWithPostgres merges into a non-empty Postgres and then back into a
// non-empty SQLite. Skipped unless YTDM_TEST_POSTGRES_URL is set.
func TestMergeBothWaysWithPostgres(t *testing.T) {
	pg := newPostgresTestStorage(t)
	src := openSQLiteForCopyTest(t, "src.db")
	seedCopyFixture(t, src)
	seedMergeTarget(t, pg)

	if _, err := MergeDatabase(src, pg); err != nil {
		t.Fatalf("sqlite -> postgres merge: %v", err)
	}
	assertMergedFixture(t, src, pg)
	if err := pg.AddChannel(Channel{ID: "UCpgnew", URL: "u", Name: "PG new"}); err != nil {
		t.Fatalf("insert on postgres after merge: %v", err)
	}

	back := openSQLiteForCopyTest(t, "back.db")
	if err := back.AddChannel(Channel{ID: "UCbackonly", URL: "u", Name: "Back only"}); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeDatabase(pg, back); err != nil {
		t.Fatalf("postgres -> sqlite merge: %v", err)
	}
	for _, id := range []string{"UCaaa", "UCbbb", "UCkeep", "UCpgnew", "UCbackonly"} {
		if !back.HasChannel(id) {
			t.Errorf("channel %s missing after merging back", id)
		}
	}
}

func TestMigrateToAbortsWhenBeforeSwitchFails(t *testing.T) {
	live := openSQLiteForCopyTest(t, "live.db")
	seedCopyFixture(t, live)
	target := filepath.Join(t.TempDir(), "target.db")
	_, err := live.MigrateTo(Options{Dialect: DialectSQLite, SQLitePath: target}, CopyMerge, func() error {
		return errors.New("cannot save preference")
	})
	if err == nil {
		t.Fatal("expected the beforeSwitch error")
	}
	if err := live.AddChannel(Channel{ID: "UCstay", URL: "u", Name: "Stay"}); err != nil {
		t.Fatal(err)
	}
	other, err := Open(Options{Dialect: DialectSQLite, SQLitePath: target})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.HasChannel("UCstay") {
		t.Error("writes must stay on the original database when the switch is aborted")
	}
}

func TestReconcileIntoOnlyAddsAndAdvances(t *testing.T) {
	main := openSQLiteForCopyTest(t, "main.db")
	fallback := openSQLiteForCopyTest(t, "fallback.db")
	pub := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	if err := main.AddChannel(Channel{ID: "UCmain", URL: "main-url", Name: "Main", RetentionDays: 9,
		FeedVideos: []FeedVideo{{ID: "shared00001", Title: "Shared", PublishedAt: pub}}}); err != nil {
		t.Fatal(err)
	}
	// During the outage the same channel was re-added with other settings and its
	// video got downloaded; a new channel was added too.
	if err := fallback.AddChannel(Channel{ID: "UCmain", URL: "other-url", Name: "Renamed", RetentionDays: 1,
		DownloadedVideos: []DownloadedVideo{{ID: "shared00001", Title: "Shared", DownloadDate: pub, PublishDate: pub}}}); err != nil {
		t.Fatal(err)
	}
	if err := fallback.AddChannel(Channel{ID: "UCoutage", URL: "u", Name: "Added during outage"}); err != nil {
		t.Fatal(err)
	}

	if _, err := ReconcileInto(fallback, main); err != nil {
		t.Fatalf("ReconcileInto() error = %v", err)
	}
	ch := channelByID(t, main, "UCmain")
	if ch.Name != "Main" || ch.URL != "main-url" || ch.RetentionDays != 9 {
		t.Errorf("existing channel settings must not be overwritten: %+v", ch)
	}
	if len(ch.DownloadedVideos) != 1 || len(ch.FeedVideos) != 0 {
		t.Errorf("shared video should advance to downloaded: %+v", ch)
	}
	if !main.HasChannel("UCoutage") {
		t.Error("channel added during the outage should be merged in")
	}
}
