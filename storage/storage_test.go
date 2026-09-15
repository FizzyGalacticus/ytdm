package storage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testSanitizeFilename mirrors package main's sanitizeFilename (filename.go) for tests
// that need to compute a channel's on-disk directory name; the storage package cannot
// import back from package main.
var testSanitizeDirRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func testSanitizeFilename(name string) string {
	result := strings.Trim(testSanitizeDirRe.ReplaceAllString(name, "_"), "_- ")
	if result == "" {
		return "unnamed"
	}
	return result
}

func TestStorageChannelOperations(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:            "test-channel-1",
		Name:          "Test Channel",
		URL:           "https://youtube.com/@test",
		RetentionDays: 7,
	}

	if err := storage.AddChannel(channel); err != nil {
		t.Errorf("Failed to add channel: %v", err)
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Errorf("Expected 1 channel, got %d", len(channels))
	}

	if channels[0].Name != "Test Channel" {
		t.Errorf("Expected channel name 'Test Channel', got '%s'", channels[0].Name)
	}

	now := time.Now()
	if err := storage.UpdateChannelLastChecked(channel.ID, now); err != nil {
		t.Errorf("Failed to update last checked time: %v", err)
	}

	channels = storage.GetChannels()
	if channels[0].LastChecked.IsZero() {
		t.Error("Last checked time should not be zero")
	}

	if err := storage.RemoveChannel(channel.ID); err != nil {
		t.Errorf("Failed to remove channel: %v", err)
	}

	channels = storage.GetChannels()
	if len(channels) != 0 {
		t.Errorf("Expected 0 channels after removal, got %d", len(channels))
	}
}

func TestStorageVideoOperations(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	video := Video{
		ID:            "test-video-1",
		Title:         "Test Video",
		URL:           "https://youtube.com/watch?v=test123",
		RetentionDays: 30,
	}

	if err := storage.AddVideo(video); err != nil {
		t.Errorf("Failed to add video: %v", err)
	}

	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Errorf("Expected 1 video, got %d", len(videos))
	}

	if videos[0].Title != "Test Video" {
		t.Errorf("Expected video title 'Test Video', got '%s'", videos[0].Title)
	}

	if err := storage.RemoveVideo(video.ID); err != nil {
		t.Errorf("Failed to remove video: %v", err)
	}

	videos = storage.GetVideos()
	if len(videos) != 0 {
		t.Errorf("Expected 0 videos after removal, got %d", len(videos))
	}
}

func TestStorageReconcileDownloadedVideosRemovesOrphans(t *testing.T) {
	root := t.TempDir()
	dataFile := filepath.Join(root, "data.db")
	downloadDir := filepath.Join(root, "downloads")

	storage, err := NewStorage(dataFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "channel-1",
		Name: "Techno Tim",
		URL:  "https://youtube.com/@TechnoTim",
		DownloadedVideos: []DownloadedVideo{
			{ID: "keep-chan", Title: "Keep Channel Video", DownloadDate: time.Now()},
			{ID: "orphan-chan", Title: "Orphan Channel Video", DownloadDate: time.Now()},
		},
	}
	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	// A standalone video's canonical ID (used for file-matching too) is always its own
	// tracked ID -- unlike channel-owned videos, it can only ever have one download
	// record, so there's no "keep this one, orphan that one" scenario within a single
	// standalone video the way there is for a channel's DownloadedVideos list.
	video := Video{
		ID:    "keep-vid",
		Title: "Tracked Video",
		URL:   "https://youtu.be/example",
		DownloadedVideos: []DownloadedVideo{
			{ID: "keep-vid", Title: "Keep Individual Video", DownloadDate: time.Now()},
		},
	}
	if err := storage.AddVideo(video); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}
	orphanVideo := Video{
		ID:    "orphan-vid",
		Title: "Orphan Individual Video",
		URL:   "https://youtu.be/orphan",
		DownloadedVideos: []DownloadedVideo{
			{ID: "orphan-vid", Title: "Orphan Individual Video", DownloadDate: time.Now()},
		},
	}
	if err := storage.AddVideo(orphanVideo); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	channelDir := filepath.Join(downloadDir, testSanitizeFilename(channel.Name))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(channelDir, "Some Title-keep-chan.mp4"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	otherDir := filepath.Join(downloadDir, "Misc")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "Another Title-keep-vid.mp4"), []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := storage.ReconcileDownloadedVideos(downloadDir, testSanitizeFilename); err != nil {
		t.Fatalf("ReconcileDownloadedVideos() error = %v", err)
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if len(channels[0].DownloadedVideos) != 1 || channels[0].DownloadedVideos[0].ID != "keep-chan" {
		t.Fatalf("expected only keep-chan to remain, got %#v", channels[0].DownloadedVideos)
	}

	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Fatalf("expected 1 video entry, got %d", len(videos))
	}
	if len(videos[0].DownloadedVideos) != 1 || videos[0].DownloadedVideos[0].ID != "keep-vid" {
		t.Fatalf("expected only keep-vid to remain, got %#v", videos[0].DownloadedVideos)
	}

	// orphan-chan's file went missing (e.g. deleted outside the app) rather than being
	// removed by the normal retention-cleanup path, but it must still end up demoted to
	// 'pruned' -- never hard-deleted -- or it could be rediscovered by a later RSS scan
	// and actually get redownloaded. orphan-vid (standalone) has no pruned-memory
	// concept and is correctly hard-deleted instead, matching RemoveVideo's semantics.
	if !storage.IsVideoDownloaded(channel.ID, "orphan-chan") {
		t.Error("orphan-chan should be remembered as pruned after reconcile, not forgotten")
	}
	if len(channels[0].PrunedVideos) != 1 || channels[0].PrunedVideos[0].ID != "orphan-chan" {
		t.Errorf("expected orphan-chan to be demoted to pruned, got %#v", channels[0].PrunedVideos)
	}
	if storage.HasVideo("orphan-vid") {
		t.Error("orphan-vid (standalone) should have been hard-deleted, not remembered")
	}
}

func TestStorageVideoDownloadTracking(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "test-channel",
		Name: "Test Channel",
		URL:  "https://youtube.com/@test",
	}
	storage.AddChannel(channel)

	videoID := "test-video-123"

	if storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Error("Video should not be marked as downloaded initially")
	}

	if err := storage.MarkVideoAsDownloaded(channel.ID, videoID, "Test Video", time.Time{}); err != nil {
		t.Errorf("Failed to mark video as downloaded: %v", err)
	}

	if !storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Error("Video should be marked as downloaded")
	}
}

func TestStorageErrorTracking(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "test-channel",
		Name: "Test Channel",
		URL:  "https://youtube.com/@test",
	}
	storage.AddChannel(channel)

	errorMsg := "Test error message"
	storage.SetChannelError(channel.ID, errorMsg)

	channels := storage.GetChannels()
	if len(channels) == 0 {
		t.Fatal("Expected channel to exist")
	}

	if channels[0].LastError != errorMsg {
		t.Errorf("Expected error '%s', got '%s'", errorMsg, channels[0].LastError)
	}

	if channels[0].LastErrorTime.IsZero() {
		t.Error("Error time should be set")
	}

	storage.ClearChannelError(channel.ID)

	channels = storage.GetChannels()
	if channels[0].LastError != "" {
		t.Errorf("Expected error to be cleared, got '%s'", channels[0].LastError)
	}
}

func TestStoragePersistence(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage1, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "persist-test",
		Name: "Persist Test",
		URL:  "https://youtube.com/@persist",
	}
	storage1.AddChannel(channel)

	// Load a second Storage instance from the same file to verify data survives
	// across a "restart" -- a bare Exec already commits, and a fresh connection
	// against the same WAL-mode file sees committed writes immediately.
	storage2, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load storage: %v", err)
	}

	channels := storage2.GetChannels()
	if len(channels) != 1 {
		t.Errorf("Expected 1 channel after reload, got %d", len(channels))
	}

	if channels[0].Name != "Persist Test" {
		t.Errorf("Expected persisted channel name 'Persist Test', got '%s'", channels[0].Name)
	}
}

func TestStorageRemoveDownloadedVideo(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "test-channel",
		Name: "Test Channel",
		URL:  "https://youtube.com/@test",
	}
	storage.AddChannel(channel)

	videoID := "test-video-456"

	storage.MarkVideoAsDownloaded(channel.ID, videoID, "Test Video", time.Time{})
	if !storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Fatal("Video should be marked as downloaded")
	}

	err = storage.RemoveDownloadedVideo(channel.ID, videoID)
	if err != nil {
		t.Errorf("Failed to remove downloaded video: %v", err)
	}

	// Should still be considered already downloaded due to pruned history tracking.
	if !storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Error("Video should remain marked as downloaded after prune removal")
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if len(channels[0].PrunedVideos) != 1 || channels[0].PrunedVideos[0].ID != videoID {
		t.Fatalf("expected pruned history to contain %s, got %#v", videoID, channels[0].PrunedVideos)
	}
}

// TestStoragePrunedVideoSurvivesReportedScenario reproduces the exact scenario that
// motivated PurgeExpiredVideos' redesign: a channel with a cutoff date one month ago and
// a 7-day retention window, and a video from 8 days ago that got downloaded then pruned
// by the regular retention-cleanup process. That video's publish date is AFTER the
// channel's cutoff (so the cutoff-floor exemption can never apply to it) and nowhere
// near permanentBookkeepingGraceDays old, so it must survive indefinitely -- confirmed
// here even after simulating a full year passing and retention having been raised in the
// meantime (both of which would, under the old buggy design, have widened the RSS
// discovery window right back over this video).
func TestStoragePrunedVideoSurvivesReportedScenario(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	channel := Channel{
		ID:            "reported-scenario-channel",
		Name:          "Reported Scenario Channel",
		URL:           "https://youtube.com/@reportedscenario",
		RetentionDays: 7,
		CutoffDate:    now.AddDate(0, 0, -30), // "a cutoff date of one month ago"
		PrunedVideos: []PrunedVideo{
			{ID: "eight-days-old", PublishDate: now.AddDate(0, 0, -8)}, // "a video from 8 days ago"
		},
	}
	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	// Simulate a year passing, with retention later raised well past the original 7-day
	// window (the exact edit that would have widened the discovery window under the old
	// design) -- still must not be purged.
	oneYearLater := now.AddDate(1, 0, 0)
	if err := storage.UpdateChannel(channel.ID, 60, false, channel.CutoffDate, "", "", false, false); err != nil {
		t.Fatalf("UpdateChannel() error = %v", err)
	}
	if _, err := storage.PurgeExpiredVideos(oneYearLater, 0); err != nil {
		t.Fatalf("PurgeExpiredVideos() error = %v", err)
	}

	if !storage.IsVideoDownloaded(channel.ID, "eight-days-old") {
		t.Fatal("eight-days-old should still be remembered as pruned (and thus never redownloaded) a year later, even after retention was raised")
	}
	channels := storage.GetChannels()
	if len(channels) != 1 || len(channels[0].PrunedVideos) != 1 || channels[0].PrunedVideos[0].ID != "eight-days-old" {
		t.Fatalf("expected eight-days-old to remain in the pruned list, got %#v", channels)
	}
}

// TestStoragePurgeExpiredVideosEventuallyReclaimsHandledRows confirms the other half of
// the design: 'pruned'/'downloaded' rows are NOT permanent forever -- they become
// eligible for hard deletion once genuinely safe, via either of two boundaries that
// (unlike the old design) can't be invalidated by an ordinary settings edit:
// permanentBookkeepingGraceDays, or the channel's own cutoff_date acting as a floor.
func TestStoragePurgeExpiredVideosEventuallyReclaimsHandledRows(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	now := time.Now().UTC()

	channel := Channel{
		ID:            "reclaim-channel",
		Name:          "Reclaim Channel",
		URL:           "https://youtube.com/@reclaim",
		RetentionDays: 7,
		CutoffDate:    now.AddDate(0, 0, -60),
		PrunedVideos: []PrunedVideo{
			// Past permanentBookkeepingGraceDays (3 years): eligible regardless of cutoff.
			{ID: "ancient-pruned", PublishDate: now.AddDate(0, 0, -(permanentBookkeepingGraceDays + 30))},
			// Older than the channel's cutoff, but nowhere near the grace period: eligible
			// via the cutoff-floor rule alone.
			{ID: "before-cutoff-pruned", PublishDate: now.AddDate(0, 0, -90)},
			// After cutoff and well within the grace period: must survive.
			{ID: "recent-pruned", PublishDate: now.AddDate(0, 0, -8)},
		},
		DownloadedVideos: []DownloadedVideo{
			{ID: "ancient-downloaded", Title: "Ancient", DownloadDate: now.AddDate(0, 0, -(permanentBookkeepingGraceDays + 30))},
			{ID: "kept-ancient-downloaded", Title: "Kept Ancient", DownloadDate: now.AddDate(0, 0, -(permanentBookkeepingGraceDays + 30)), DisablePruning: true},
		},
	}
	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	deleted, err := storage.PurgeExpiredVideos(now, 0)
	if err != nil {
		t.Fatalf("PurgeExpiredVideos() error = %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 rows deleted (ancient-pruned, before-cutoff-pruned, ancient-downloaded), got %d", deleted)
	}

	if storage.IsVideoDownloaded(channel.ID, "ancient-pruned") {
		t.Error("ancient-pruned should have been purged (past the fixed grace period)")
	}
	if storage.IsVideoDownloaded(channel.ID, "before-cutoff-pruned") {
		t.Error("before-cutoff-pruned should have been purged (older than the channel's cutoff floor)")
	}
	if !storage.IsVideoDownloaded(channel.ID, "recent-pruned") {
		t.Error("recent-pruned must survive: after cutoff and within the grace period")
	}
	if storage.IsVideoDownloaded(channel.ID, "ancient-downloaded") {
		t.Error("ancient-downloaded should have been purged (past the fixed grace period)")
	}
	if !storage.IsVideoDownloaded(channel.ID, "kept-ancient-downloaded") {
		t.Error("kept-ancient-downloaded has disable_pruning and should never be purged")
	}
}

func TestStorageUpdateChannelDownloadedVideoPruning(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "channel-prune-test",
		Name: "Prune Toggle Channel",
		DownloadedVideos: []DownloadedVideo{
			{ID: "vid123", Title: "Video 123", DownloadDate: time.Now().AddDate(0, 0, -5)},
		},
	}

	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	if err := storage.UpdateChannelDownloadedVideoPruning(channel.ID, "vid123", true); err != nil {
		t.Fatalf("UpdateChannelDownloadedVideoPruning() error = %v", err)
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("Expected 1 channel, got %d", len(channels))
	}
	if len(channels[0].DownloadedVideos) != 1 {
		t.Fatalf("Expected 1 downloaded video, got %d", len(channels[0].DownloadedVideos))
	}
	if !channels[0].DownloadedVideos[0].DisablePruning {
		t.Fatalf("Expected downloaded video pruning to be disabled")
	}
}

func TestStorageUpdateChannel(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:            "test-channel",
		Name:          "Test Channel",
		URL:           "https://youtube.com/@test",
		RetentionDays: 7,
		VideoQuality:  "720",
		VideoFormat:   "mp4",
	}
	storage.AddChannel(channel)

	newRetention := 14
	newDisablePruning := true
	newCutoff := time.Now().AddDate(0, 0, -14)
	newQuality := "1080"
	newFormat := "webm"
	newShorts := true

	err = storage.UpdateChannel(channel.ID, newRetention, newDisablePruning, newCutoff, newQuality, newFormat, newShorts, false)
	if err != nil {
		t.Errorf("Failed to update channel: %v", err)
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatal("Expected channel to exist")
	}

	updated := channels[0]
	if updated.RetentionDays != newRetention {
		t.Errorf("Expected retention days %d, got %d", newRetention, updated.RetentionDays)
	}
	if updated.DisablePruning != newDisablePruning {
		t.Errorf("Expected disable pruning %v, got %v", newDisablePruning, updated.DisablePruning)
	}
	if updated.VideoQuality != newQuality {
		t.Errorf("Expected quality %s, got %s", newQuality, updated.VideoQuality)
	}
	if updated.VideoFormat != newFormat {
		t.Errorf("Expected format %s, got %s", newFormat, updated.VideoFormat)
	}
	if updated.DownloadShorts != newShorts {
		t.Errorf("Expected shorts %v, got %v", newShorts, updated.DownloadShorts)
	}
}

func TestStorageUpdateVideoLastChecked(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	video := Video{
		ID:    "test-video",
		Title: "Test Video",
		URL:   "https://youtube.com/watch?v=test",
	}
	storage.AddVideo(video)

	checkTime := time.Now()
	err = storage.UpdateVideoLastChecked(video.ID, checkTime)
	if err != nil {
		t.Errorf("Failed to update video last checked: %v", err)
	}

	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Fatal("Expected video to exist")
	}

	if videos[0].LastChecked.IsZero() {
		t.Error("Last checked time should not be zero")
	}
}

func TestStorageUpdateVideo(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	video := Video{
		ID:            "video-edit-test",
		Title:         "Editable Video",
		URL:           "https://youtube.com/watch?v=video-edit-test",
		RetentionDays: 7,
		VideoQuality:  "720",
		VideoFormat:   "mp4",
	}

	if err := storage.AddVideo(video); err != nil {
		t.Fatalf("Failed to add video: %v", err)
	}

	newRetention := 30
	newDisablePruning := true
	newQuality := "1080"
	newFormat := "webm"
	newShorts := true

	err = storage.UpdateVideo(video.ID, newRetention, newDisablePruning, newQuality, newFormat, newShorts)
	if err != nil {
		t.Errorf("Failed to update video: %v", err)
	}

	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Fatalf("Expected 1 video, got %d", len(videos))
	}

	updated := videos[0]
	if updated.RetentionDays != newRetention {
		t.Errorf("Expected retention days %d, got %d", newRetention, updated.RetentionDays)
	}
	if updated.DisablePruning != newDisablePruning {
		t.Errorf("Expected disable pruning %v, got %v", newDisablePruning, updated.DisablePruning)
	}
	if updated.VideoQuality != newQuality {
		t.Errorf("Expected quality %s, got %s", newQuality, updated.VideoQuality)
	}
	if updated.VideoFormat != newFormat {
		t.Errorf("Expected format %s, got %s", newFormat, updated.VideoFormat)
	}
	if updated.DownloadShorts != newShorts {
		t.Errorf("Expected shorts %v, got %v", newShorts, updated.DownloadShorts)
	}
}

func TestStorageConcurrency(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "concurrent-test",
		Name: "Concurrent Test",
		URL:  "https://youtube.com/@concurrent",
	}
	storage.AddChannel(channel)

	done := make(chan bool, 10)

	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = storage.GetChannels()
			}
			done <- true
		}()
	}

	for i := 0; i < 5; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				videoID := "video-" + string(rune('0'+id))
				storage.MarkVideoAsDownloaded(channel.ID, videoID, "Test Video", time.Time{})
				storage.IsVideoDownloaded(channel.ID, videoID)
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Errorf("Expected 1 channel after concurrent operations, got %d", len(channels))
	}
}

func TestStorageVideoErrorTracking(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	video := Video{
		ID:    "test-video",
		Title: "Test Video",
		URL:   "https://youtube.com/watch?v=test",
	}
	storage.AddVideo(video)

	errorMsg := "Download failed: network timeout"
	storage.SetVideoError(video.ID, errorMsg)

	videos := storage.GetVideos()
	if len(videos) == 0 {
		t.Fatal("Expected video to exist")
	}

	if videos[0].LastError != errorMsg {
		t.Errorf("Expected error '%s', got '%s'", errorMsg, videos[0].LastError)
	}

	if videos[0].LastErrorTime.IsZero() {
		t.Error("Error time should be set")
	}

	storage.ClearVideoError(video.ID)

	videos = storage.GetVideos()
	if videos[0].LastError != "" {
		t.Errorf("Expected error to be cleared, got '%s'", videos[0].LastError)
	}
}

func TestStorageEdgeCases(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	t.Run("update non-existent channel returns no error", func(t *testing.T) {
		err := storage.UpdateChannelLastChecked("non-existent", time.Now())
		if err != nil {
			t.Errorf("Expected no error when updating non-existent channel, got: %v", err)
		}
	})

	t.Run("remove non-existent channel returns no error", func(t *testing.T) {
		err := storage.RemoveChannel("non-existent")
		if err != nil {
			t.Errorf("Expected no error when removing non-existent channel, got: %v", err)
		}
	})

	t.Run("update non-existent video returns no error", func(t *testing.T) {
		err := storage.UpdateVideoLastChecked("non-existent", time.Now())
		if err != nil {
			t.Errorf("Expected no error when updating non-existent video, got: %v", err)
		}
	})

	t.Run("remove non-existent video returns no error", func(t *testing.T) {
		err := storage.RemoveVideo("non-existent")
		if err != nil {
			t.Errorf("Expected no error when removing non-existent video, got: %v", err)
		}
	})

	t.Run("mark video downloaded for non-existent channel returns no error", func(t *testing.T) {
		err := storage.MarkVideoAsDownloaded("non-existent-channel", "video-id", "Test Video", time.Time{})
		if err != nil {
			t.Errorf("Expected no error when marking video downloaded for non-existent channel, got: %v", err)
		}
	})

	t.Run("check video downloaded for non-existent channel", func(t *testing.T) {
		isDownloaded := storage.IsVideoDownloaded("non-existent-channel", "video-id")
		if isDownloaded {
			t.Error("Expected false for non-existent channel")
		}
	})

	// The normalized schema enforces channel/video ID uniqueness via real constraints
	// (channel_sources/video_sources primary keys) instead of the old flat-JSON model's
	// silent duplicate-ID acceptance -- a deliberate integrity improvement from the
	// migration, not a regression.
	t.Run("duplicate channel ID is rejected", func(t *testing.T) {
		channel := Channel{
			ID:   "duplicate-test",
			Name: "Duplicate Test",
			URL:  "https://youtube.com/@duplicate",
		}

		if err := storage.AddChannel(channel); err != nil {
			t.Errorf("First add failed: %v", err)
		}

		channel2 := channel
		channel2.Name = "Different Name"
		if err := storage.AddChannel(channel2); err == nil {
			t.Error("expected an error adding a channel with a duplicate ID, got nil")
		}

		channels := storage.GetChannels()
		count := 0
		for _, ch := range channels {
			if ch.ID == "duplicate-test" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("Expected exactly 1 channel with the ID (duplicate rejected), got %d", count)
		}
	})

	t.Run("duplicate video ID is rejected", func(t *testing.T) {
		video := Video{
			ID:    "duplicate-video",
			Title: "Duplicate Video",
			URL:   "https://youtube.com/watch?v=dup",
		}

		if err := storage.AddVideo(video); err != nil {
			t.Errorf("First add failed: %v", err)
		}

		video2 := video
		video2.Title = "Different Title"
		if err := storage.AddVideo(video2); err == nil {
			t.Error("expected an error adding a video with a duplicate ID, got nil")
		}

		videos := storage.GetVideos()
		count := 0
		for _, vid := range videos {
			if vid.ID == "duplicate-video" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("Expected exactly 1 video with the ID (duplicate rejected), got %d", count)
		}
	})
}

func TestStorageMigrateChannelIDs(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	t.Run("migrate old handle-style IDs to canonical UC IDs", func(t *testing.T) {
		channel1 := Channel{
			ID:   "@philipdefranco",
			URL:  "https://www.youtube.com/@philipdefranco",
			Name: "Philip DeFranco",
		}
		channel2 := Channel{
			ID:   "UCxauM3N4Nb47HG6PuDMh0Ow", // Already proper UC format
			URL:  "https://www.youtube.com/channel/UCxauM3N4Nb47HG6PuDMh0Ow",
			Name: "Pete Buttigieg",
		}
		channel3 := Channel{
			ID:   "@camerongray",
			URL:  "https://www.youtube.com/@camerongray",
			Name: "Cameron Gray",
		}

		storage.AddChannel(channel1)
		storage.AddChannel(channel2)
		storage.AddChannel(channel3)

		migratedCount := 0

		channels := storage.GetChannels()
		for _, ch := range channels {
			if !strings.HasPrefix(ch.ID, "UC") {
				var newID string
				switch ch.ID {
				case "@philipdefranco":
					newID = "UClFSU9_bUb4Rc6OYfTt5SPw"
				case "@camerongray":
					newID = "UCsiayKhhnd-iJLpqVpFLa3w"
				default:
					continue
				}

				if err := storage.UpdateChannelID(ch.ID, newID); err != nil {
					t.Fatalf("Failed to update channel ID: %v", err)
				}
				migratedCount++
			}
		}

		if migratedCount != 2 {
			t.Errorf("Expected 2 channels to be migrated, got %d", migratedCount)
		}

		updatedChannels := storage.GetChannels()
		philipFound := false
		peteFound := false
		cameraFound := false

		for _, ch := range updatedChannels {
			if ch.Name == "Philip DeFranco" {
				philipFound = true
				if ch.ID != "UClFSU9_bUb4Rc6OYfTt5SPw" {
					t.Errorf("Philip DeFranco ID not updated correctly: got %s", ch.ID)
				}
			}
			if ch.Name == "Pete Buttigieg" {
				peteFound = true
				if ch.ID != "UCxauM3N4Nb47HG6PuDMh0Ow" {
					t.Errorf("Pete Buttigieg ID changed unexpectedly: got %s", ch.ID)
				}
			}
			if ch.Name == "Cameron Gray" {
				cameraFound = true
				if ch.ID != "UCsiayKhhnd-iJLpqVpFLa3w" {
					t.Errorf("Cameron Gray ID not updated correctly: got %s", ch.ID)
				}
			}
		}

		if !philipFound || !peteFound || !cameraFound {
			t.Errorf("Not all expected channels found after migration")
		}
	})

	t.Run("no migration needed when all channels have UC IDs", func(t *testing.T) {
		tmpFile2 := filepath.Join(t.TempDir(), "test_data2.db")
		storage2, err := NewStorage(tmpFile2)
		if err != nil {
			t.Fatalf("Failed to create storage: %v", err)
		}

		channel1 := Channel{
			ID:   "UCxauM3N4Nb47HG6PuDMh0Ow",
			URL:  "https://www.youtube.com/channel/UCxauM3N4Nb47HG6PuDMh0Ow",
			Name: "Channel 1",
		}
		channel2 := Channel{
			ID:   "UClFSU9_bUb4Rc6OYfTt5SPw",
			URL:  "https://www.youtube.com/channel/UClFSU9_bUb4Rc6OYfTt5SPw",
			Name: "Channel 2",
		}

		storage2.AddChannel(channel1)
		storage2.AddChannel(channel2)

		needsMigration := false
		for _, ch := range storage2.GetChannels() {
			if !strings.HasPrefix(ch.ID, "UC") {
				needsMigration = true
				break
			}
		}

		if needsMigration {
			t.Errorf("Should have no channels needing migration")
		}
	})
}

func TestRemoveVideoRemovesSingleEntry(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")
	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	video := Video{
		ID:    "video-to-remove",
		Title: "Test Video",
		URL:   "https://youtube.com/watch?v=test123",
	}

	if err := storage.AddVideo(video); err != nil {
		t.Fatalf("Failed to add video: %v", err)
	}

	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Errorf("Expected 1 video, got %d", len(videos))
	}

	if err := storage.RemoveVideo("video-to-remove"); err != nil {
		t.Errorf("Failed to remove video: %v", err)
	}

	videos = storage.GetVideos()
	if len(videos) != 0 {
		t.Errorf("Expected 0 videos after removal, got %d", len(videos))
	}
}

func TestRemoveVideoDoesNotAffectOtherVideos(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")
	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	video1 := Video{
		ID:    "video-1",
		Title: "Video 1",
		URL:   "https://youtube.com/watch?v=vid1",
	}
	video2 := Video{
		ID:    "video-2",
		Title: "Video 2",
		URL:   "https://youtube.com/watch?v=vid2",
	}

	if err := storage.AddVideo(video1); err != nil {
		t.Fatalf("Failed to add video1: %v", err)
	}
	if err := storage.AddVideo(video2); err != nil {
		t.Fatalf("Failed to add video2: %v", err)
	}

	videos := storage.GetVideos()
	if len(videos) != 2 {
		t.Errorf("Expected 2 videos, got %d", len(videos))
	}

	if err := storage.RemoveVideo("video-1"); err != nil {
		t.Errorf("Failed to remove video1: %v", err)
	}

	videos = storage.GetVideos()
	if len(videos) != 1 {
		t.Errorf("Expected 1 video after removal, got %d", len(videos))
	}
	if videos[0].ID != "video-2" {
		t.Errorf("Expected remaining video to be video-2, got %s", videos[0].ID)
	}
}

func TestRemoveVideoErrorOnNonexistent(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")
	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// RemoveVideo succeeds silently for a nonexistent ID; this test documents that behavior.
	if err := storage.RemoveVideo("nonexistent-id"); err != nil {
		t.Errorf("expected no error removing nonexistent video, got %v", err)
	}
}

func TestSetChannelThumbnailIfEmpty(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "data.db")
	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	ch := Channel{ID: "UCthumb1", Name: "Thumb Channel"}
	if err := storage.AddChannel(ch); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	if err := storage.SetChannelThumbnailIfEmpty("UCthumb1", "https://example.com/icon.jpg"); err != nil {
		t.Fatalf("SetChannelThumbnailIfEmpty() error = %v", err)
	}
	channels := storage.GetChannels()
	if channels[0].ThumbnailURL != "https://example.com/icon.jpg" {
		t.Errorf("ThumbnailURL = %q, want %q", channels[0].ThumbnailURL, "https://example.com/icon.jpg")
	}

	if err := storage.SetChannelThumbnailIfEmpty("UCthumb1", "https://example.com/other.jpg"); err != nil {
		t.Fatalf("second SetChannelThumbnailIfEmpty() error = %v", err)
	}
	channels = storage.GetChannels()
	if channels[0].ThumbnailURL != "https://example.com/icon.jpg" {
		t.Errorf("ThumbnailURL overwritten to %q, want original", channels[0].ThumbnailURL)
	}

	if err := storage.SetChannelThumbnailIfEmpty("UCunknown", "https://example.com/nope.jpg"); err != nil {
		t.Errorf("expected no error for unknown channel, got %v", err)
	}

	if err := storage.SetChannelThumbnailIfEmpty("UCthumb1", ""); err != nil {
		t.Errorf("expected no error for empty url, got %v", err)
	}
}

func TestMergeChannelDownloadedVideos(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "data.db")
	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	ch := Channel{
		ID:   "UCmerge1",
		Name: "Merge Channel",
		DownloadedVideos: []DownloadedVideo{
			{ID: "existing-vid", Title: "Existing Video"},
		},
	}
	if err := storage.AddChannel(ch); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	incoming := []DownloadedVideo{
		{ID: "existing-vid", Title: "Should Be Skipped"},
		{ID: "new-vid-1", Title: "New Video 1"},
		{ID: "new-vid-2", Title: "New Video 2"},
	}
	if err := storage.MergeChannelDownloadedVideos("UCmerge1", incoming); err != nil {
		t.Fatalf("MergeChannelDownloadedVideos() error = %v", err)
	}
	channels := storage.GetChannels()
	if len(channels[0].DownloadedVideos) != 3 {
		t.Fatalf("expected 3 downloaded videos, got %d: %+v", len(channels[0].DownloadedVideos), channels[0].DownloadedVideos)
	}
	ids := map[string]bool{}
	for _, dv := range channels[0].DownloadedVideos {
		ids[dv.ID] = true
	}
	for _, wantID := range []string{"existing-vid", "new-vid-1", "new-vid-2"} {
		if !ids[wantID] {
			t.Errorf("missing expected video ID %q", wantID)
		}
	}

	if err := storage.MergeChannelDownloadedVideos("UCmerge1", nil); err != nil {
		t.Errorf("expected no error for empty slice, got %v", err)
	}

	err = storage.MergeChannelDownloadedVideos("UCunknown", []DownloadedVideo{{ID: "x"}})
	if err == nil {
		t.Error("expected error for unknown channel, got nil")
	}
}

func TestStorageDismissAllFeedVideos(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "dismiss-all-channel",
		Name: "Dismiss All Channel",
		URL:  "https://youtube.com/@dismissall",
		FeedVideos: []FeedVideo{
			{ID: "feed-1", Title: "Feed 1", PublishedAt: time.Now()},
			{ID: "feed-2", Title: "Feed 2", PublishedAt: time.Now()},
		},
		DownloadedVideos: []DownloadedVideo{
			{ID: "downloaded-1", Title: "Downloaded 1"},
		},
	}
	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	count, err := storage.DismissAllFeedVideos(channel.ID)
	if err != nil {
		t.Fatalf("DismissAllFeedVideos() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 videos dismissed, got %d", count)
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if len(channels[0].FeedVideos) != 0 {
		t.Fatalf("expected no pending feed videos left, got %#v", channels[0].FeedVideos)
	}
	if len(channels[0].PrunedVideos) != 2 {
		t.Fatalf("expected 2 pruned videos, got %#v", channels[0].PrunedVideos)
	}
	if len(channels[0].DownloadedVideos) != 1 {
		t.Fatalf("expected the already-downloaded video to be untouched, got %#v", channels[0].DownloadedVideos)
	}

	// Calling again with nothing pending is a no-op.
	count, err = storage.DismissAllFeedVideos(channel.ID)
	if err != nil {
		t.Fatalf("DismissAllFeedVideos() second call error = %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 videos dismissed on second call, got %d", count)
	}
}

// TestStoragePurgeExpiredVideos confirms PurgeExpiredVideos' scope is now restricted to
// 'pending' rows only: 'downloaded' and 'pruned' rows must survive no matter their age,
// since both carry the "never redownload" guarantee, which no window-derived age
// threshold can safely enforce (retention_days/cutoff_date are user-editable, so a
// video correctly purged under today's window could otherwise resurface and actually
// get redownloaded if that window widens later -- see AddPrunedVideo's doc comment).
func TestStoragePurgeExpiredVideos(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	now := time.Now().UTC()
	longAgo := now.AddDate(0, 0, -30)  // > 2x a 7-day retention window
	recently := now.AddDate(0, 0, -10) // < 2x a 7-day retention window

	channel := Channel{
		ID:            "purge-channel",
		Name:          "Purge Channel",
		URL:           "https://youtube.com/@purge",
		RetentionDays: 7,
		DownloadedVideos: []DownloadedVideo{
			{ID: "old-downloaded", Title: "Old But Downloaded", DownloadDate: longAgo},
		},
		PrunedVideos: []PrunedVideo{
			{ID: "old-pruned", PublishDate: longAgo},
		},
		FeedVideos: []FeedVideo{
			{ID: "expired-pending", Title: "Expired Pending", PublishedAt: longAgo},
			{ID: "fresh-pending", Title: "Fresh Pending", PublishedAt: recently},
		},
	}
	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	noExpiryChannel := Channel{
		ID:   "no-expiry-channel",
		Name: "No Expiry Channel",
		URL:  "https://youtube.com/@noexpiry",
		FeedVideos: []FeedVideo{
			{ID: "no-expiry-pending", Title: "No Expiry", PublishedAt: longAgo},
		},
	}
	if err := storage.AddChannel(noExpiryChannel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	disabledChannel := Channel{
		ID:             "disabled-pruning-channel",
		Name:           "Disabled Pruning Channel",
		URL:            "https://youtube.com/@disabled",
		RetentionDays:  7,
		DisablePruning: true,
		FeedVideos: []FeedVideo{
			{ID: "disabled-channel-pending", Title: "Disabled Channel", PublishedAt: longAgo},
		},
	}
	if err := storage.AddChannel(disabledChannel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	deleted, err := storage.PurgeExpiredVideos(now, 0)
	if err != nil {
		t.Fatalf("PurgeExpiredVideos() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 row deleted (only expired-pending), got %d", deleted)
	}

	if !storage.IsVideoDownloaded(channel.ID, "old-downloaded") {
		t.Error("old-downloaded must never be purged: it's a permanent 'downloaded' record")
	}
	if !storage.IsVideoDownloaded(channel.ID, "old-pruned") {
		t.Error("old-pruned must never be purged: it's a permanent 'pruned' record")
	}

	channels := storage.GetChannels()
	var found *Channel
	for i := range channels {
		if channels[i].ID == channel.ID {
			found = &channels[i]
		}
	}
	if found == nil {
		t.Fatal("channel not found")
	}
	if len(found.DownloadedVideos) != 1 || len(found.PrunedVideos) != 1 {
		t.Fatalf("expected downloaded/pruned records untouched, got %#v", found)
	}
	if len(found.FeedVideos) != 1 || found.FeedVideos[0].ID != "fresh-pending" {
		t.Fatalf("expected only expired-pending removed, got %#v", found.FeedVideos)
	}

	for _, ch := range channels {
		if ch.ID == noExpiryChannel.ID && len(ch.FeedVideos) != 1 {
			t.Errorf("no-expiry-pending has no retention configured and should not have been purged, got %#v", ch.FeedVideos)
		}
		if ch.ID == disabledChannel.ID && len(ch.FeedVideos) != 1 {
			t.Errorf("disabled-channel-pending belongs to a disable_pruning channel and should not have been purged, got %#v", ch.FeedVideos)
		}
	}
}
