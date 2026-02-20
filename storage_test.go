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
	if err := storage.MarkVideoAsDownloaded(channel.ID, videoID); err != nil {
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
