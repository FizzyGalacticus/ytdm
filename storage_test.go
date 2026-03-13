package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageChannelOperations(t *testing.T) {
	// Create a temporary file for testing
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Test adding a channel
	channel := Channel{
		ID:            "test-channel-1",
		Name:          "Test Channel",
		URL:           "https://youtube.com/@test",
		RetentionDays: 7,
	}

	if err := storage.AddChannel(channel); err != nil {
		t.Errorf("Failed to add channel: %v", err)
	}

	// Test retrieving channels
	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Errorf("Expected 1 channel, got %d", len(channels))
	}

	if channels[0].Name != "Test Channel" {
		t.Errorf("Expected channel name 'Test Channel', got '%s'", channels[0].Name)
	}

	// Test updating last checked time
	now := time.Now()
	if err := storage.UpdateChannelLastChecked(channel.ID, now); err != nil {
		t.Errorf("Failed to update last checked time: %v", err)
	}

	channels = storage.GetChannels()
	if channels[0].LastChecked.IsZero() {
		t.Error("Last checked time should not be zero")
	}

	// Test removing a channel
	if err := storage.RemoveChannel(channel.ID); err != nil {
		t.Errorf("Failed to remove channel: %v", err)
	}

	channels = storage.GetChannels()
	if len(channels) != 0 {
		t.Errorf("Expected 0 channels after removal, got %d", len(channels))
	}
}

func TestStorageVideoOperations(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Test adding a video
	video := Video{
		ID:            "test-video-1",
		Title:         "Test Video",
		URL:           "https://youtube.com/watch?v=test123",
		RetentionDays: 30,
	}

	if err := storage.AddVideo(video); err != nil {
		t.Errorf("Failed to add video: %v", err)
	}

	// Test retrieving videos
	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Errorf("Expected 1 video, got %d", len(videos))
	}

	if videos[0].Title != "Test Video" {
		t.Errorf("Expected video title 'Test Video', got '%s'", videos[0].Title)
	}

	// Test removing a video
	if err := storage.RemoveVideo(video.ID); err != nil {
		t.Errorf("Failed to remove video: %v", err)
	}

	videos = storage.GetVideos()
	if len(videos) != 0 {
		t.Errorf("Expected 0 videos after removal, got %d", len(videos))
	}
}

func TestStorageVideoDownloadTracking(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Create a channel first (required for video download tracking)
	channel := Channel{
		ID:   "test-channel",
		Name: "Test Channel",
		URL:  "https://youtube.com/@test",
	}
	storage.AddChannel(channel)

	videoID := "test-video-123"

	// Initially should not be downloaded
	if storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Error("Video should not be marked as downloaded initially")
	}

	// Mark as downloaded
	if err := storage.MarkVideoAsDownloaded(channel.ID, videoID, "Test Video"); err != nil {
		t.Errorf("Failed to mark video as downloaded: %v", err)
	}

	// Should now be downloaded
	if !storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Error("Video should be marked as downloaded")
	}
}

func TestStorageErrorTracking(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Add a channel
	channel := Channel{
		ID:   "test-channel",
		Name: "Test Channel",
		URL:  "https://youtube.com/@test",
	}
	storage.AddChannel(channel)

	// Set error
	errorMsg := "Test error message"
	storage.SetChannelError(channel.ID, errorMsg)

	// Check error was set
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

	// Clear error
	storage.ClearChannelError(channel.ID)

	channels = storage.GetChannels()
	if channels[0].LastError != "" {
		t.Errorf("Expected error to be cleared, got '%s'", channels[0].LastError)
	}
}

func TestStoragePersistence(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	// Create storage and add data
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

	// Save happens automatically in AddChannel, so just load from file
	// Load storage from same file
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

	// Cleanup
	os.Remove(tmpFile)
}

func TestStorageRemoveDownloadedVideo(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Add channel
	channel := Channel{
		ID:   "test-channel",
		Name: "Test Channel",
		URL:  "https://youtube.com/@test",
	}
	storage.AddChannel(channel)

	videoID := "test-video-456"

	// Mark video as downloaded
	storage.MarkVideoAsDownloaded(channel.ID, videoID, "Test Video")
	if !storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Fatal("Video should be marked as downloaded")
	}

	// Remove the downloaded video
	err = storage.RemoveDownloadedVideo(channel.ID, videoID)
	if err != nil {
		t.Errorf("Failed to remove downloaded video: %v", err)
	}

	// Should no longer be marked as downloaded
	if storage.IsVideoDownloaded(channel.ID, videoID) {
		t.Error("Video should not be marked as downloaded after removal")
	}
}

func TestStorageUpdateChannel(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Add channel
	channel := Channel{
		ID:            "test-channel",
		Name:          "Test Channel",
		URL:           "https://youtube.com/@test",
		RetentionDays: 7,
		VideoQuality:  "720",
		VideoFormat:   "mp4",
	}
	storage.AddChannel(channel)

	// Update channel settings
	newRetention := 14
	newCutoff := time.Now().AddDate(0, 0, -14)
	newQuality := "1080"
	newFormat := "webm"
	newShorts := true

	err = storage.UpdateChannel(channel.ID, newRetention, newCutoff, newQuality, newFormat, newShorts)
	if err != nil {
		t.Errorf("Failed to update channel: %v", err)
	}

	// Verify updates
	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatal("Expected channel to exist")
	}

	updated := channels[0]
	if updated.RetentionDays != newRetention {
		t.Errorf("Expected retention days %d, got %d", newRetention, updated.RetentionDays)
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
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Add video
	video := Video{
		ID:    "test-video",
		Title: "Test Video",
		URL:   "https://youtube.com/watch?v=test",
	}
	storage.AddVideo(video)

	// Update last checked time
	checkTime := time.Now()
	err = storage.UpdateVideoLastChecked(video.ID, checkTime)
	if err != nil {
		t.Errorf("Failed to update video last checked: %v", err)
	}

	// Verify update
	videos := storage.GetVideos()
	if len(videos) != 1 {
		t.Fatal("Expected video to exist")
	}

	if videos[0].LastChecked.IsZero() {
		t.Error("Last checked time should not be zero")
	}
}

func TestStorageConcurrency(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Add a channel
	channel := Channel{
		ID:   "concurrent-test",
		Name: "Concurrent Test",
		URL:  "https://youtube.com/@concurrent",
	}
	storage.AddChannel(channel)

	// Simulate concurrent reads and writes
	done := make(chan bool, 10)

	// 5 readers
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = storage.GetChannels()
			}
			done <- true
		}()
	}

	// 5 writers
	for i := 0; i < 5; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				videoID := "video-" + string(rune('0'+id))
				storage.MarkVideoAsDownloaded(channel.ID, videoID, "Test Video")
				storage.IsVideoDownloaded(channel.ID, videoID)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify storage is still consistent
	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Errorf("Expected 1 channel after concurrent operations, got %d", len(channels))
	}
}

func TestStorageVideoErrorTracking(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

	storage, err := NewStorage(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}

	// Add video
	video := Video{
		ID:    "test-video",
		Title: "Test Video",
		URL:   "https://youtube.com/watch?v=test",
	}
	storage.AddVideo(video)

	// Set error
	errorMsg := "Download failed: network timeout"
	storage.SetVideoError(video.ID, errorMsg)

	// Check error was set
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

	// Clear error
	storage.ClearVideoError(video.ID)

	videos = storage.GetVideos()
	if videos[0].LastError != "" {
		t.Errorf("Expected error to be cleared, got '%s'", videos[0].LastError)
	}
}

func TestStorageEdgeCases(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_data.json")

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
		err := storage.MarkVideoAsDownloaded("non-existent-channel", "video-id", "Test Video")
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

	t.Run("duplicate channel addition allowed", func(t *testing.T) {
		channel := Channel{
			ID:   "duplicate-test",
			Name: "Duplicate Test",
			URL:  "https://youtube.com/@duplicate",
		}

		err := storage.AddChannel(channel)
		if err != nil {
			t.Errorf("First add failed: %v", err)
		}

		// Add again with same ID - should succeed (no duplicate checking)
		channel2 := channel
		channel2.Name = "Different Name"
		err = storage.AddChannel(channel2)
		if err != nil {
			t.Errorf("Second add failed: %v", err)
		}

		// Should have 2 channels with same ID
		channels := storage.GetChannels()
		count := 0
		for _, ch := range channels {
			if ch.ID == "duplicate-test" {
				count++
			}
		}
		if count != 2 {
			t.Errorf("Expected 2 channels with same ID, got %d", count)
		}
	})

	t.Run("duplicate video addition allowed", func(t *testing.T) {
		video := Video{
			ID:    "duplicate-video",
			Title: "Duplicate Video",
			URL:   "https://youtube.com/watch?v=dup",
		}

		err := storage.AddVideo(video)
		if err != nil {
			t.Errorf("First add failed: %v", err)
		}

		// Add again with same ID - should succeed (no duplicate checking)
		video2 := video
		video2.Title = "Different Title"
		err = storage.AddVideo(video2)
		if err != nil {
			t.Errorf("Second add failed: %v", err)
		}

		// Should have 2 videos with same ID
		videos := storage.GetVideos()
		count := 0
		for _, vid := range videos {
			if vid.ID == "duplicate-video" {
				count++
			}
		}
		if count != 2 {
			t.Errorf("Expected 2 videos with same ID, got %d", count)
		}
	})
}
