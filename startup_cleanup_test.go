package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ytdm/storage"
)

func TestRunStartupChannelPruneScanAt_PrunesByRetentionFromDownloadDate(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	config.DownloadDir = tmpDir
	config.RetentionDays = 30
	config.DisablePruning = false

	storagePath := filepath.Join(tmpDir, "data.db")
	store, err := storage.NewStorage(storagePath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channel := storage.Channel{
		ID:            "UCchannel001",
		Name:          "Channel One",
		RetentionDays: 30,
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "abc123", Title: "Old Video"},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	channelDir := filepath.Join(tmpDir, sanitizeFilename(channel.Name))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	oldMedia := filepath.Join(channelDir, "2024-01-01 old-video-abc123.mp4")
	oldInfo := filepath.Join(channelDir, "2024-01-01 old-video-abc123.info.json")
	for _, p := range []string{oldMedia, oldInfo} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", p, err)
		}
	}

	oldDownloadDate := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, p := range []string{oldMedia, oldInfo} {
		if err := os.Chtimes(p, oldDownloadDate, oldDownloadDate); err != nil {
			t.Fatalf("Chtimes(%s) error = %v", p, err)
		}
	}

	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	result := runStartupChannelPruneScanAt(now, config, store)

	if result.VideosPruned != 1 {
		t.Fatalf("expected 1 video pruned, got %d", result.VideosPruned)
	}
	if result.FilesRemoved != 2 {
		t.Fatalf("expected 2 files removed, got %d", result.FilesRemoved)
	}

	if _, err := os.Stat(oldMedia); !os.IsNotExist(err) {
		t.Fatalf("expected media file removed, stat err = %v", err)
	}
	if _, err := os.Stat(oldInfo); !os.IsNotExist(err) {
		t.Fatalf("expected info file removed, stat err = %v", err)
	}

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected one channel, got %d", len(channels))
	}
	if len(channels[0].DownloadedVideos) != 0 {
		t.Fatalf("expected downloaded video entry removed from storage, still have %d", len(channels[0].DownloadedVideos))
	}
}

func TestRunStartupChannelPruneScanAt_DoesNotPruneByCutoffDate(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	config.DownloadDir = tmpDir
	config.RetentionDays = 3650

	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	channel := storage.Channel{
		ID:         "UCchannel002",
		Name:       "Channel Two",
		CutoffDate: cutoff,
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "vid999", Title: "Before Cutoff"},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	channelDir := filepath.Join(tmpDir, sanitizeFilename(channel.Name))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	oldFile := filepath.Join(channelDir, "2024-12-31 before-cutoff-vid999.mp4")
	if err := os.WriteFile(oldFile, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	recentDownloadDate := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldFile, recentDownloadDate, recentDownloadDate); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	result := runStartupChannelPruneScanAt(time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC), config, store)
	if result.VideosPruned != 0 {
		t.Fatalf("expected no prune by cutoff date, got %d", result.VideosPruned)
	}

	if _, err := os.Stat(oldFile); err != nil {
		t.Fatalf("expected cutoff-violating file to remain when download date is recent, stat err = %v", err)
	}
}

func TestRunStartupChannelPruneScanAt_KeepsRecentFiles(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	config.DownloadDir = tmpDir
	config.RetentionDays = 30

	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channel := storage.Channel{
		ID:            "UCchannel003",
		Name:          "Channel Three",
		RetentionDays: 30,
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "new123", Title: "Recent Video"},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	channelDir := filepath.Join(tmpDir, sanitizeFilename(channel.Name))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	recentFile := filepath.Join(channelDir, "2026-04-25 recent-new123.mp4")
	if err := os.WriteFile(recentFile, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	recentDownloadDate := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(recentFile, recentDownloadDate, recentDownloadDate); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	result := runStartupChannelPruneScanAt(time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC), config, store)
	if result.VideosPruned != 0 || result.FilesRemoved != 0 {
		t.Fatalf("expected no prune for recent file, got videos=%d files=%d", result.VideosPruned, result.FilesRemoved)
	}

	if _, err := os.Stat(recentFile); err != nil {
		t.Fatalf("expected recent file to remain, stat err = %v", err)
	}

	channels := store.GetChannels()
	if len(channels) != 1 || len(channels[0].DownloadedVideos) != 1 {
		t.Fatalf("expected tracked video to remain in storage")
	}
}

func TestRunStartupChannelPruneScanAt_PrunesFromLegacyChannelPath(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	config.DownloadDir = tmpDir
	config.RetentionDays = 30

	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channel := storage.Channel{
		ID:            "UClegacy001",
		Name:          "Channel With Unicode é",
		RetentionDays: 30,
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "legacy123", Title: "Legacy Video"},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	legacyDir := filepath.Join(tmpDir, legacySanitizeFilename(channel.Name))
	currentDir := filepath.Join(tmpDir, sanitizeFilename(channel.Name))

	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("MkdirAll(legacyDir) error = %v", err)
	}

	oldLegacyFile := filepath.Join(legacyDir, "2024-01-01 legacy-video-legacy123.mp4")
	if err := os.WriteFile(oldLegacyFile, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile(legacy file) error = %v", err)
	}
	oldDownloadDate := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldLegacyFile, oldDownloadDate, oldDownloadDate); err != nil {
		t.Fatalf("Chtimes(legacy file) error = %v", err)
	}

	result := runStartupChannelPruneScanAt(time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC), config, store)
	if result.VideosPruned != 1 {
		t.Fatalf("expected one pruned video from legacy dir, got %d", result.VideosPruned)
	}
	if result.FilesRemoved != 1 {
		t.Fatalf("expected one removed file from legacy dir, got %d", result.FilesRemoved)
	}

	if _, err := os.Stat(oldLegacyFile); !os.IsNotExist(err) {
		t.Fatalf("expected legacy-path file removed, stat err = %v", err)
	}

	if legacyDir != currentDir {
		if _, err := os.Stat(currentDir); err == nil {
			t.Fatalf("did not expect current normalized dir to be created when only legacy dir existed")
		}
	}
}

func TestRunStartupChannelPruneScanAt_NoPruneMovesToSanitizedLocation(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	config.DownloadDir = tmpDir
	config.RetentionDays = 30

	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channel := storage.Channel{
		ID:            "UCnoprun001",
		Name:          "Legacy Path Channel é",
		RetentionDays: 30,
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "keep123", Title: "Keep Video", DisablePruning: true},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	legacyDir := filepath.Join(tmpDir, legacySanitizeFilename(channel.Name))
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("MkdirAll(legacyDir) error = %v", err)
	}

	legacyFileName := "2024-01-01 Legacy Vídeo! keep123.mp4"
	legacyPath := filepath.Join(legacyDir, legacyFileName)
	if err := os.WriteFile(legacyPath, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile(legacy no-prune file) error = %v", err)
	}

	result := runStartupChannelPruneScanAt(time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC), config, store)
	if result.VideosPruned != 0 {
		t.Fatalf("expected no videos pruned for DisablePruning entry, got %d", result.VideosPruned)
	}
	if result.FilesRemoved != 0 {
		t.Fatalf("expected no files removed for DisablePruning entry, got %d", result.FilesRemoved)
	}
	if result.FilesMoved != 1 {
		t.Fatalf("expected one file moved for DisablePruning entry, got %d", result.FilesMoved)
	}

	targetDir := filepath.Join(tmpDir, sanitizeFilename(channel.Name))
	targetPath := filepath.Join(targetDir, "2024-01-01 LegacyVdeo-keep123.mp4")

	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected old legacy-path file to be moved, stat err = %v", err)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("expected no-prune file moved to sanitized location, stat err = %v", err)
	}

	channels := store.GetChannels()
	if len(channels) != 1 || len(channels[0].DownloadedVideos) != 1 {
		t.Fatalf("expected no-prune tracked entry to remain in storage")
	}
}

func TestExistingChannelDirs_IncludesRawLegacyAndCurrent(t *testing.T) {
	tmpDir := t.TempDir()
	name := "Mixed Name é"

	rawDir := filepath.Join(tmpDir, name)
	legacyDir := filepath.Join(tmpDir, legacySanitizeFilename(name))
	currentDir := filepath.Join(tmpDir, sanitizeFilename(name))

	for _, d := range []string{rawDir, legacyDir, currentDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", d, err)
		}
	}

	dirs := existingChannelDirs(tmpDir, name)
	if len(dirs) < 1 {
		t.Fatalf("expected at least one channel dir, got %d", len(dirs))
	}

	want := map[string]bool{rawDir: false, legacyDir: false, currentDir: false}
	for _, d := range dirs {
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}

	for dir, found := range want {
		if !found {
			t.Fatalf("expected directory candidate %s to be included", dir)
		}
	}
}
