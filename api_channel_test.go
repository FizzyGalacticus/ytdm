package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAddChannelResolvesCanonicalChannelID(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "data.json")
	storage, err := NewStorage(dataPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	script := filepath.Join(tmpDir, "fake-yt-dlp.sh")
	scriptContent := "#!/bin/sh\necho '{\"channel_id\":\"UCresolved123\"}'\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to create fake yt-dlp: %v", err)
	}

	cfg := DefaultConfig()
	cfg.YtDlp.Path = script

	api := &APIServer{config: cfg, storage: storage}

	payload := map[string]interface{}{
		"url":  "https://www.youtube.com/@somehandle",
		"name": "Some Channel",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	api.addChannel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("addChannel status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if channels[0].ID != "UCresolved123" {
		t.Fatalf("stored channel ID = %q, want %q", channels[0].ID, "UCresolved123")
	}
}

func TestUpdateChannelDownloadedVideoPruning(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "data.json")
	storage, err := NewStorage(dataPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channel := Channel{
		ID:   "UCprunetest",
		Name: "Prune Toggle",
		DownloadedVideos: []DownloadedVideo{
			{ID: "vidABC", Title: "Tracked Video"},
		},
	}
	if err := storage.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	api := &APIServer{config: DefaultConfig(), storage: storage}

	payload := map[string]bool{"disable_pruning": true}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/UCprunetest/videos/vidABC", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	api.handleChannelByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update downloaded video pruning status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := storage.GetChannels()
	if len(channels) != 1 || len(channels[0].DownloadedVideos) != 1 {
		t.Fatalf("unexpected channels/downloaded_videos shape: %+v", channels)
	}
	if !channels[0].DownloadedVideos[0].DisablePruning {
		t.Fatalf("expected disable_pruning true on downloaded video")
	}
}

func TestConvertToChannelCreatesNew(t *testing.T) {
	tmpDir := t.TempDir()
	storage, err := NewStorage(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	// Add two individual videos from the same uploader
	for _, v := range []Video{
		{ID: "vid-1", Title: "Video 1", Uploader: "Test Creator", UploaderID: "UCtest123",
			DownloadedVideos: []DownloadedVideo{{ID: "dv-1", Title: "Video 1"}}},
		{ID: "vid-2", Title: "Video 2", Uploader: "Test Creator", UploaderID: "UCtest123",
			DownloadedVideos: []DownloadedVideo{{ID: "dv-2", Title: "Video 2"}}},
	} {
		if err := storage.AddVideo(v); err != nil {
			t.Fatalf("AddVideo() error = %v", err)
		}
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	api := &APIServer{config: cfg, storage: storage}

	body, _ := json.Marshal(map[string]interface{}{
		"uploader_name":  "Test Creator",
		"uploader_id":    "UCtest123",
		"video_ids":      []string{"vid-1", "vid-2"},
		"video_quality":  "720",
		"retention_days": 14,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos/convert-to-channel", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleConvertToChannel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleConvertToChannel() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch := channels[0]
	if ch.ID != "UCtest123" {
		t.Errorf("channel ID = %q, want %q", ch.ID, "UCtest123")
	}
	if ch.Name != "Test Creator" {
		t.Errorf("channel Name = %q, want %q", ch.Name, "Test Creator")
	}
	if ch.VideoQuality != "720" {
		t.Errorf("VideoQuality = %q, want %q", ch.VideoQuality, "720")
	}
	if ch.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", ch.RetentionDays)
	}
	if len(ch.DownloadedVideos) != 2 {
		t.Errorf("DownloadedVideos count = %d, want 2", len(ch.DownloadedVideos))
	}

	// Individual video entries should be removed
	videos := storage.GetVideos()
	if len(videos) != 0 {
		t.Errorf("expected 0 individual video entries, got %d", len(videos))
	}
}

func TestConvertToChannelMergesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	storage, err := NewStorage(filepath.Join(tmpDir, "data.json"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	// Pre-existing channel with one tracked download
	existingChannel := Channel{
		ID:               "UCmergetest",
		Name:             "Merge Creator",
		URL:              "https://www.youtube.com/channel/UCmergetest",
		DownloadedVideos: []DownloadedVideo{{ID: "existing-dv", Title: "Already There"}},
	}
	if err := storage.AddChannel(existingChannel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	// Individual video entry with a new download
	if err := storage.AddVideo(Video{
		ID: "solo-vid", Title: "Solo", Uploader: "Merge Creator", UploaderID: "UCmergetest",
		DownloadedVideos: []DownloadedVideo{{ID: "new-dv", Title: "New Download"}},
	}); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	api := &APIServer{config: cfg, storage: storage}

	body, _ := json.Marshal(map[string]interface{}{
		"uploader_name": "Merge Creator",
		"uploader_id":   "UCmergetest",
		"video_ids":     []string{"solo-vid"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos/convert-to-channel", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleConvertToChannel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleConvertToChannel() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Still exactly one channel (no duplicate created)
	channels := storage.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	// Both downloads should be present
	if len(channels[0].DownloadedVideos) != 2 {
		t.Errorf("DownloadedVideos count = %d, want 2; got %+v", len(channels[0].DownloadedVideos), channels[0].DownloadedVideos)
	}

	// Individual video entry should be removed
	videos := storage.GetVideos()
	if len(videos) != 0 {
		t.Errorf("expected 0 individual video entries, got %d", len(videos))
	}
}
