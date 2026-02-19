package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// VideoInfo represents metadata about a video
type VideoInfo struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	UploadDate  string    `json:"upload_date"`
	Uploader    string    `json:"uploader"`
	UploaderID  string    `json:"uploader_id"`
	PublishTime time.Time `json:"-"`
}

// Downloader handles yt-dlp operations
type Downloader struct {
	config *Config
}

// NewDownloader creates a new Downloader instance
func NewDownloader(config *Config) *Downloader {
	return &Downloader{config: config}
}

// buildYtDlpCommand creates a base yt-dlp command with cookies configured
func (d *Downloader) buildYtDlpCommand(args ...string) *exec.Cmd {
	cmdArgs := args

	// Add user-agent and anti-throttling options
	antiThrottle := []string{
		"--user-agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36",
		"--sleep-requests", "3",      // 3 seconds between requests
		"--socket-timeout", "30",     // 30 second socket timeout
		"--extractor-args", "youtube:lang=en",
	}
	cmdArgs = append(antiThrottle, cmdArgs...)

	// Add cookies support if configured
	if d.config.CookiesBrowser != "" {
		cmdArgs = append([]string{"--cookies-from-browser", d.config.CookiesBrowser}, cmdArgs...)
	} else if d.config.CookiesFile != "" {
		cmdArgs = append([]string{"--cookies", d.config.CookiesFile}, cmdArgs...)
	}

	return exec.Command(d.config.YtDlpPath, cmdArgs...)
}

// GetChannelVideos retrieves metadata for all videos from a channel
func (d *Downloader) GetChannelVideos(channelURL string, since time.Time) ([]VideoInfo, error) {
	log.Printf("Fetching video list from channel: %s", channelURL)

	// Use yt-dlp to get video information in JSON format
	cmd := d.buildYtDlpCommand(
		"--dump-json",
		"--skip-download",
		"--playlist-end", "50", // Limit to recent 50 videos
		channelURL,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("yt-dlp failed: %v, stderr: %s", err, stderr.String())
	}

	// Parse JSON output (one JSON object per line)
	var videos []VideoInfo
	lines := strings.Split(stdout.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}

		var info VideoInfo
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			log.Printf("Failed to parse video info: %v", err)
			continue
		}

		// Parse upload date
		if info.UploadDate != "" {
			t, err := time.Parse("20060102", info.UploadDate)
			if err == nil {
				info.PublishTime = t
				// Only include videos published after 'since' time
				if t.After(since) {
					videos = append(videos, info)
				}
			}
		}
	}

	log.Printf("Found %d new videos from channel", len(videos))
	return videos, nil
}

// GetVideoInfo retrieves metadata for a specific video
func (d *Downloader) GetVideoInfo(videoURL string) (*VideoInfo, error) {
	log.Printf("Fetching video info: %s", videoURL)

	cmd := d.buildYtDlpCommand(
		"--dump-json",
		videoURL,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("yt-dlp failed: %v, stderr: %s", err, stderr.String())
	}

	var info VideoInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("failed to parse video info: %v", err)
	}

	// Parse upload date
	if info.UploadDate != "" {
		t, err := time.Parse("20060102", info.UploadDate)
		if err == nil {
			info.PublishTime = t
		}
	}

	return &info, nil
}

// DownloadVideo downloads a video to the specified directory
func (d *Downloader) DownloadVideo(videoURL, channelName string) error {
	// Create channel subdirectory
	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	log.Printf("Downloading video: %s to %s", videoURL, channelDir)

	// Build yt-dlp command
	cmd := d.buildYtDlpCommand(
		"-o", filepath.Join(channelDir, d.config.FileNamePattern),
		"--no-playlist",
		videoURL,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("yt-dlp download failed: %v, stderr: %s", err, stderr.String())
	}

	log.Printf("Successfully downloaded video: %s", videoURL)
	return nil
}

// CleanOldVideosForChannel removes videos older than the retention period for a specific channel
func (d *Downloader) CleanOldVideosForChannel(channelName string, retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}

	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
	if _, err := os.Stat(channelDir); os.IsNotExist(err) {
		return nil // Channel directory doesn't exist yet
	}

	log.Printf("Cleaning old videos for channel %s (retention: %d days)", channelName, retentionDays)
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	return filepath.Walk(channelDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files with errors
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Check if file is older than cutoff
		if info.ModTime().Before(cutoffTime) {
			log.Printf("Removing old video: %s (modified: %s)", path, info.ModTime())
			if err := os.Remove(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			}
		}

		return nil
	})
}

// CleanOldVideos removes videos older than the retention period (legacy method for global cleanup)
func (d *Downloader) CleanOldVideos() error {
	log.Printf("Cleaning old videos (retention: %d days)", d.config.RetentionDays)

	if d.config.RetentionDays <= 0 {
		return nil
	}

	cutoffTime := time.Now().AddDate(0, 0, -d.config.RetentionDays)

	return filepath.Walk(d.config.DownloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files with errors
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Check if file is older than cutoff
		if info.ModTime().Before(cutoffTime) {
			log.Printf("Removing old video: %s (modified: %s)", path, info.ModTime())
			if err := os.Remove(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			}
		}

		return nil
	})
}

// sanitizeFilename removes or replaces characters that are invalid in filenames
func sanitizeFilename(name string) string {
	// Replace invalid characters with underscores
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := name
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "_")
	}
	return result
}
