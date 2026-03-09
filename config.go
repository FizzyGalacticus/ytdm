package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// YtDlpConfig holds yt-dlp specific configuration
type YtDlpConfig struct {
	Path                    string `json:"path"`
	UpdateInterval          string `json:"update_interval_seconds"`          // Go duration string (e.g., "24h0m0s")
	CookiesBrowser          string `json:"cookies_browser"`                  // firefox, chrome, or empty to disable
	CookiesFile             string `json:"cookies_file"`                     // path to cookies.txt file
	ExtractorSleepInterval  string `json:"extractor_sleep_interval_seconds"` // Go duration string
	DownloadThroughputLimit string `json:"download_throughput_limit"`
	RestrictFilenames       bool   `json:"restrict_filenames"`
	CacheDir                string `json:"cache_dir"`
}

// Config holds the application configuration
type Config struct {
	sync.RWMutex
	CheckInterval      string      `json:"check_interval_seconds"` // Go duration string (e.g., "5m0s")
	RetentionDays      int         `json:"retention_days"`
	DownloadDir        string      `json:"download_dir"`
	FileNamePattern    string      `json:"file_name_pattern"`
	APIPort            int         `json:"api_port"`
	MaxConcurrent      int         `json:"max_concurrent_downloads"`
	DefaultVideoFormat string      `json:"default_video_format"` // mp4, webm, mkv
	YtDlp              YtDlpConfig `json:"yt_dlp"`
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		CheckInterval:      "5m0s",
		RetentionDays:      7,
		DownloadDir:        "/downloads",
		FileNamePattern:    "%(upload_date>%Y-%m-%d)s %(title)s-%(id)s.%(ext)s",
		APIPort:            8080,
		MaxConcurrent:      3,
		DefaultVideoFormat: "mp4",
		YtDlp: YtDlpConfig{
			Path:                    "yt-dlp",
			UpdateInterval:          "24h0m0s",
			ExtractorSleepInterval:  "0s",
			DownloadThroughputLimit: "",
			RestrictFilenames:       false,
			CacheDir:                "data/yt-dlp-cache",
		},
	}
}

// LoadConfig loads configuration from a JSON file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	applyConfigDefaults(&config)

	return &config, nil
}

// Save saves the configuration to a JSON file
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// ReloadFromDisk reloads configuration from disk into the in-memory config
func (c *Config) ReloadFromDisk(path string) error {
	c.Lock()
	defer c.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, c); err != nil {
		return err
	}

	applyConfigDefaults(c)

	return nil
}

func applyConfigDefaults(c *Config) {
	if c.YtDlp.Path == "" {
		c.YtDlp.Path = "yt-dlp"
	}
	if c.YtDlp.CacheDir == "" {
		c.YtDlp.CacheDir = "data/yt-dlp-cache"
	}
	if c.CheckInterval == "" {
		c.CheckInterval = "5m0s"
	}
	if c.YtDlp.UpdateInterval == "" {
		c.YtDlp.UpdateInterval = "24h0m0s"
	}
	if c.YtDlp.ExtractorSleepInterval == "" {
		c.YtDlp.ExtractorSleepInterval = "0s"
	}
	if c.DefaultVideoFormat == "" {
		c.DefaultVideoFormat = "mp4"
	}
}

// GetCheckInterval returns the current check interval as time.Duration with locking
func (c *Config) GetCheckInterval() time.Duration {
	c.RLock()
	defer c.RUnlock()
	dur, _ := time.ParseDuration(c.CheckInterval)
	return dur
}

// GetUpdateInterval returns the current yt-dlp update interval as time.Duration with locking
func (c *Config) GetUpdateInterval() time.Duration {
	c.RLock()
	defer c.RUnlock()
	dur, _ := time.ParseDuration(c.YtDlp.UpdateInterval)
	return dur
}

// GetExtractorSleepInterval returns the extractor sleep interval as time.Duration with locking
func (c *Config) GetExtractorSleepInterval() time.Duration {
	c.RLock()
	defer c.RUnlock()
	dur, _ := time.ParseDuration(c.YtDlp.ExtractorSleepInterval)
	return dur
}
