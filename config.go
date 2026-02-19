package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Config holds the application configuration
type Config struct {
	sync.RWMutex
	CheckInterval       time.Duration `json:"check_interval_seconds"`
	RetentionDays       int           `json:"retention_days"`
	DownloadDir         string        `json:"download_dir"`
	FileNamePattern     string        `json:"file_name_pattern"`
	APIPort             int           `json:"api_port"`
	MaxConcurrent       int           `json:"max_concurrent_downloads"`
	YtDlpPath           string        `json:"yt_dlp_path"`
	YtDlpUpdateInterval time.Duration `json:"yt_dlp_update_interval_seconds"`
	CookiesBrowser      string        `json:"cookies_browser"` // firefox, chrome, or empty to disable
	CookiesFile         string        `json:"cookies_file"`   // path to cookies.txt file
}

// configJSON is used for JSON marshaling with seconds instead of duration
type configJSON struct {
	CheckIntervalSeconds       int    `json:"check_interval_seconds"`
	RetentionDays              int    `json:"retention_days"`
	DownloadDir                string `json:"download_dir"`
	FileNamePattern            string `json:"file_name_pattern"`
	APIPort                    int    `json:"api_port"`
	MaxConcurrent              int    `json:"max_concurrent_downloads"`
	YtDlpPath                  string `json:"yt_dlp_path"`
	YtDlpUpdateIntervalSeconds int    `json:"yt_dlp_update_interval_seconds"`
	CookiesBrowser             string `json:"cookies_browser"`
	CookiesFile                string `json:"cookies_file"`
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		CheckInterval:       5 * time.Minute,
		RetentionDays:       7,
		DownloadDir:         "/downloads",
		FileNamePattern:     "%(title)s-%(id)s.%(ext)s",
		APIPort:             8080,
		MaxConcurrent:       3,
		YtDlpPath:           "yt-dlp",
		YtDlpUpdateInterval: 24 * time.Hour,
	}
}

// LoadConfig loads configuration from a JSON file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cj configJSON
	if err := json.Unmarshal(data, &cj); err != nil {
		return nil, err
	}

	return &Config{
		CheckInterval:       time.Duration(cj.CheckIntervalSeconds) * time.Second,
		RetentionDays:       cj.RetentionDays,
		DownloadDir:         cj.DownloadDir,
		FileNamePattern:     cj.FileNamePattern,
		APIPort:             cj.APIPort,
		MaxConcurrent:       cj.MaxConcurrent,
		YtDlpPath:           cj.YtDlpPath,
		YtDlpUpdateInterval: time.Duration(cj.YtDlpUpdateIntervalSeconds) * time.Second,
		CookiesBrowser:      cj.CookiesBrowser,
		CookiesFile:         cj.CookiesFile,
	}, nil
}

// Save saves the configuration to a JSON file
func (c *Config) Save(path string) error {
	cj := configJSON{
		CheckIntervalSeconds:       int(c.CheckInterval.Seconds()),
		RetentionDays:              c.RetentionDays,
		DownloadDir:                c.DownloadDir,
		FileNamePattern:            c.FileNamePattern,
		APIPort:                    c.APIPort,
		MaxConcurrent:              c.MaxConcurrent,
		YtDlpPath:                  c.YtDlpPath,
		YtDlpUpdateIntervalSeconds: int(c.YtDlpUpdateInterval.Seconds()),
		CookiesBrowser:             c.CookiesBrowser,
		CookiesFile:                c.CookiesFile,
	}

	data, err := json.MarshalIndent(cj, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// ReloadConfig reloads configuration from disk into the in-memory config
func (c *Config) ReloadFromDisk(path string) error {
	c.Lock()
	defer c.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var cj configJSON
	if err := json.Unmarshal(data, &cj); err != nil {
		return err
	}

	// Update config fields with lock held
	c.CheckInterval = time.Duration(cj.CheckIntervalSeconds) * time.Second
	c.RetentionDays = cj.RetentionDays
	c.DownloadDir = cj.DownloadDir
	c.FileNamePattern = cj.FileNamePattern
	c.APIPort = cj.APIPort
	c.MaxConcurrent = cj.MaxConcurrent
	c.YtDlpPath = cj.YtDlpPath
	c.YtDlpUpdateInterval = time.Duration(cj.YtDlpUpdateIntervalSeconds) * time.Second
	c.CookiesBrowser = cj.CookiesBrowser
	c.CookiesFile = cj.CookiesFile

	return nil
}

// GetCheckInterval returns the current check interval with locking
func (c *Config) GetCheckInterval() time.Duration {
	c.RLock()
	defer c.RUnlock()
	return c.CheckInterval
}

// GetUpdateInterval returns the current yt-dlp update interval with locking
func (c *Config) GetUpdateInterval() time.Duration {
	c.RLock()
	defer c.RUnlock()
	return c.YtDlpUpdateInterval
}
