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
