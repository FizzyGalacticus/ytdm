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

func TestStorageTrimPrunedVideos(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.db")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	channel := Channel{
		ID:   "trim-channel",
		Name: "Trim Channel",
		URL:  "https://youtube.com/@trim",
		PrunedVideos: []PrunedVideo{
			{ID: "old-vid", PublishDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
			{ID: "recent-vid", PublishDate: time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)},
			{ID: "no-date-vid", PublishDate: time.Time{}}, // zero date: never evict
		},
	}
	storage.AddChannel(channel)

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // old-vid predates this

	if err := storage.TrimPrunedVideos(channel.ID, since); err != nil {
		t.Fatalf("TrimPrunedVideos() error = %v", err)
	}

	channels := storage.GetChannels()
	var found *Channel
	for i := range channels {
		if channels[i].ID == channel.ID {
			found = &channels[i]
		}
	}
	if found == nil {
		t.Fatal("channel not found after trim")
	}

	if len(found.PrunedVideos) != 2 {
		t.Fatalf("expected 2 pruned videos after trim, got %d: %v", len(found.PrunedVideos), found.PrunedVideos)
	}
	for _, pv := range found.PrunedVideos {
		if pv.ID == "old-vid" {
			t.Error("old-vid should have been evicted")
		}
	}
	if storage.IsVideoDownloaded(channel.ID, "old-vid") {
		t.Error("old-vid should no longer be considered downloaded after trim")
	}
	if !storage.IsVideoDownloaded(channel.ID, "recent-vid") {
		t.Error("recent-vid should still be considered downloaded after trim")
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
