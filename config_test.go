package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	config := DefaultConfig()

	if config.CheckInterval != 5*time.Minute {
		t.Errorf("Expected default check interval 5m, got %v", config.CheckInterval)
	}

	if config.RetentionDays != 7 {
		t.Errorf("Expected default retention days 7, got %d", config.RetentionDays)
	}

	if config.MaxConcurrent != 3 {
		t.Errorf("Expected default max concurrent 3, got %d", config.MaxConcurrent)
	}

	if config.YtDlpUpdateInterval != 24*time.Hour {
		t.Errorf("Expected default update interval 24h, got %v", config.YtDlpUpdateInterval)
	}
}

func TestConfigLoadSave(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_config.json")

	// Create config
	config := DefaultConfig()
	config.RetentionDays = 30
	config.MaxConcurrent = 5
	config.CookiesFile = "test_cookies.txt"

	// Save to file
	if err := config.Save(tmpFile); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Load from file
	loadedConfig, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify values
	if loadedConfig.RetentionDays != 30 {
		t.Errorf("Expected retention days 30, got %d", loadedConfig.RetentionDays)
	}

	if loadedConfig.MaxConcurrent != 5 {
		t.Errorf("Expected max concurrent 5, got %d", loadedConfig.MaxConcurrent)
	}

	if loadedConfig.CookiesFile != "test_cookies.txt" {
		t.Errorf("Expected cookies file 'test_cookies.txt', got '%s'", loadedConfig.CookiesFile)
	}

	// Cleanup
	os.Remove(tmpFile)
}

func TestConfigReload(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_config.json")

	// Create initial config
	config := DefaultConfig()
	config.RetentionDays = 10
	config.Save(tmpFile)

	// Load config
	loadedConfig, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Modify on disk
	modifiedConfig := DefaultConfig()
	modifiedConfig.RetentionDays = 20
	modifiedConfig.Save(tmpFile)

	// Reload
	if err := loadedConfig.ReloadFromDisk(tmpFile); err != nil {
		t.Fatalf("Failed to reload config: %v", err)
	}

	// Verify updated value
	loadedConfig.RLock()
	retentionDays := loadedConfig.RetentionDays
	loadedConfig.RUnlock()
	
	if retentionDays != 20 {
		t.Errorf("Expected retention days 20 after reload, got %d", retentionDays)
	}

	// Cleanup
	os.Remove(tmpFile)
}

func TestConfigThreadSafety(t *testing.T) {
	config := DefaultConfig()

	// Test concurrent reads/writes
	done := make(chan bool)
	iterations := 100

	// Writer goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			config.Lock()
			config.RetentionDays = i
			config.Unlock()
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			config.RLock()
			_ = config.RetentionDays
			config.RUnlock()
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// If we get here without deadlock or race, test passes
}

func TestConfigGetters(t *testing.T) {
	config := DefaultConfig()
	config.CheckInterval = 10 * time.Minute
	config.YtDlpUpdateInterval = 2 * time.Hour

	if config.GetCheckInterval() != 10*time.Minute {
		t.Errorf("Expected check interval 10m, got %v", config.GetCheckInterval())
	}

	if config.GetUpdateInterval() != 2*time.Hour {
		t.Errorf("Expected update interval 2h, got %v", config.GetUpdateInterval())
	}

	config.RLock()
	retentionDays := config.RetentionDays
	config.RUnlock()

	if retentionDays != 7 {
		t.Errorf("Expected retention days 7, got %d", retentionDays)
	}
}

func TestConfigCookieSettings(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test_config.json")

	config := DefaultConfig()
	config.CookiesBrowser = "firefox"
	config.CookiesFile = ""

	config.Save(tmpFile)

	loadedConfig, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if loadedConfig.CookiesBrowser != "firefox" {
		t.Errorf("Expected cookies browser 'firefox', got '%s'", loadedConfig.CookiesBrowser)
	}

	// Test file-based cookies
	config.CookiesBrowser = ""
	config.CookiesFile = "data/cookies.txt"
	config.Save(tmpFile)

	loadedConfig2, err := LoadConfig(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if loadedConfig2.CookiesFile != "data/cookies.txt" {
		t.Errorf("Expected cookies file 'data/cookies.txt', got '%s'", loadedConfig2.CookiesFile)
	}

	// Cleanup
	os.Remove(tmpFile)
}
