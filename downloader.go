package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var jitterMu sync.Mutex

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

	// Add user-agent and base options
	baseOptions := []string{
		"--user-agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36",
		"--socket-timeout", "30", // 30 second socket timeout
		"--extractor-args", "youtube:lang=en",
		"--windows-filenames",
		"--quiet",
	}

	if d.config.YtDlp.RestrictFilenames {
		baseOptions = append(baseOptions, "--restrict-filenames")
	}

	if d.config.YtDlp.CacheDir != "" {
		baseOptions = append(baseOptions, "--cache-dir", d.config.YtDlp.CacheDir)
	}

	if d.config.YtDlp.DownloadThroughputLimit != "" {
		baseOptions = append(baseOptions, "--limit-rate", d.config.YtDlp.DownloadThroughputLimit)
	}

	sleepInterval := d.config.GetExtractorSleepInterval()
	if sleepInterval > 0 {
		sleepSeconds := int(sleepInterval.Seconds())
		jittered := addJitterSeconds(sleepSeconds, 0.5)
		baseOptions = append(baseOptions,
			"--sleep-requests", fmt.Sprintf("%d", jittered),
			"--sleep-interval", fmt.Sprintf("%d", jittered),
			"--sleep-subtitles", fmt.Sprintf("%d", jittered),
		)
	}

	cmdArgs = append(baseOptions, cmdArgs...)

	// Add cookies support if configured
	if d.config.YtDlp.CookiesBrowser != "" {
		cmdArgs = append([]string{"--cookies-from-browser", d.config.YtDlp.CookiesBrowser}, cmdArgs...)
	} else if d.config.YtDlp.CookiesFile != "" {
		cmdArgs = append([]string{"--cookies", d.config.YtDlp.CookiesFile}, cmdArgs...)
	}

	return exec.Command(d.config.YtDlp.Path, cmdArgs...)
}

func addJitterSeconds(baseSeconds int, jitterPercent float64) int {
	if baseSeconds <= 0 {
		return 0
	}

	maxJitter := int(float64(baseSeconds) * jitterPercent)
	if maxJitter <= 0 {
		return baseSeconds
	}

	jitterMu.Lock()
	jitter := jitterRand.Intn(maxJitter + 1)
	jitterMu.Unlock()

	return baseSeconds + jitter
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

// buildFormatString constructs a yt-dlp format string based on desired quality
// quality can be "best", a specific height like "720", "480", "360", or empty for defaults
func (d *Downloader) buildFormatString(quality string) string {
	quality = strings.TrimSpace(quality)

	if quality == "" || quality == "best" {
		// Default: prefer mp4 format, fallback to best available
		return "best[ext=mp4]/best"
	}

	// Try to match specific quality (assume it's a height like "720", "480", etc.)
	// Format: bestvideo[height<=720][ext=mp4]+bestaudio/best[ext=mp4]/best
	return fmt.Sprintf("bestvideo[height<=%s][ext=mp4]+bestaudio/best[ext=mp4][height<=%s]/best", quality, quality)
}

// DownloadVideo downloads a video to the specified directory
func (d *Downloader) DownloadVideo(videoURL, channelName, quality string, downloadShorts bool) error {
	// Create channel subdirectory
	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	log.Printf("Downloading video: %s to %s (quality: %s, downloadShorts: %v)", videoURL, channelDir, quality, downloadShorts)

	// First, fetch metadata with yt-dlp
	metadata, err := d.fetchVideoMetadata(videoURL)
	if err != nil {
		log.Printf("Warning: could not fetch metadata for %s: %v", videoURL, err)
		metadata = nil // Continue with download even if metadata fails
	}

	// Build format string based on desired quality
	formatStr := d.buildFormatString(quality)

	// Build match filters based on shorts preference
	var matchFilter string
	if downloadShorts {
		// Allow shorts: only exclude live streams and very short videos
		matchFilter = "!is_live & duration>60"
	} else {
		// Exclude shorts: exclude live streams, short videos, and vertical aspect ratio
		matchFilter = "!is_live & duration>60 & width>=height"
	}

	// Build yt-dlp command for download
	cmd := d.buildYtDlpCommand(
		"-o", filepath.Join(channelDir, d.config.FileNamePattern),
		"--no-playlist",
		"-f", formatStr,
		"--match-filters", matchFilter,
		"--embed-chapters",
		"--write-info-json",
		videoURL,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("yt-dlp download failed: %v, stderr: %s", err, stderr.String())
	}

	log.Printf("Successfully downloaded video: %s", videoURL)

	// Generate NFO file if metadata was available
	if metadata != nil {
		if err := d.generateNFOFile(channelDir, metadata); err != nil {
			log.Printf("Warning: failed to generate NFO file: %v", err)
		}
	}

	return nil
}

// CleanOldVideosForChannel removes videos older than the retention period for a specific channel
// using download dates stored in persistence rather than file modification times
// Also removes stale entries from the downloaded list if files no longer exist
func (d *Downloader) CleanOldVideosForChannel(channelName, channelID string, retentionDays int, storage *Storage) error {
	if retentionDays <= 0 {
		return nil
	}

	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
	if _, err := os.Stat(channelDir); os.IsNotExist(err) {
		return nil // Channel directory doesn't exist yet
	}

	log.Printf("Cleaning old videos for channel %s (retention: %d days)", channelName, retentionDays)
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	// Get list of downloaded videos to check against
	channels := storage.GetChannels()
	var channelData *Channel
	for _, ch := range channels {
		if ch.ID == channelID {
			channelData = &ch
			break
		}
	}

	if channelData == nil {
		return nil // Channel not found
	}

	// First, remove stale entries (where files don't exist)
	for _, vid := range channelData.DownloadedVideos {
		fileFound := false
		// Check if any file contains this video ID
		entries, err := os.ReadDir(channelDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.Contains(entry.Name(), vid.ID) {
					fileFound = true
					break
				}
			}
		}

		// If file doesn't exist but is recorded, remove the entry
		if !fileFound {
			log.Printf("Removing stale entry for video %s (file not found)", vid.ID)
			storage.RemoveDownloadedVideo(channelID, vid.ID)
		}
	}

	// Then, delete old files from disk
	return filepath.Walk(channelDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files with errors
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		baseName := filepath.Base(path)

		// Find the video ID by checking all downloaded videos for this channel
		var foundDownloadDate time.Time
		fallbackToMtime := true

		for _, vid := range channelData.DownloadedVideos {
			// Check if the video ID appears in the filename
			if strings.Contains(baseName, vid.ID) {
				foundDownloadDate = vid.DownloadDate
				fallbackToMtime = false
				break
			}
		}

		// Check if file is older than cutoff
		var timeToCheck time.Time
		if !fallbackToMtime && !foundDownloadDate.IsZero() {
			timeToCheck = foundDownloadDate
		} else {
			// Fallback to file modification time if we couldn't find in storage
			timeToCheck = info.ModTime()
		}

		if timeToCheck.Before(cutoffTime) {
			log.Printf("Removing old video: %s (download date: %s)", path, timeToCheck)
			if err := os.Remove(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			}
		}

		return nil
	})
}

// CleanOldVideosForVideo removes videos older than the retention period for a specific individual video entry
// using download dates stored in persistence rather than file modification times
// Also removes stale entries from the downloaded list if files no longer exist
func (d *Downloader) CleanOldVideosForVideo(videoTitle, videoID string, retentionDays int, storage *Storage) error {
	if retentionDays <= 0 {
		return nil
	}

	log.Printf("Cleaning old videos for individual video %s (retention: %d days)", videoTitle, retentionDays)
	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)

	// Get the video entry to check its downloaded videos
	videos := storage.GetVideos()
	var videoEntry *Video
	for i := range videos {
		if videos[i].ID == videoID {
			videoEntry = &videos[i]
			break
		}
	}

	if videoEntry == nil {
		return nil // Video entry not found
	}

	// First, remove stale entries (where files don't exist anywhere in downloads)
	for _, vid := range videoEntry.DownloadedVideos {
		fileFound := false
		// Check if any file in the downloads directory contains this video ID
		entries, err := os.ReadDir(d.config.DownloadDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.Contains(entry.Name(), vid.ID) {
					fileFound = true
					break
				} else if entry.IsDir() {
					// Also check subdirectories
					subEntries, err := os.ReadDir(filepath.Join(d.config.DownloadDir, entry.Name()))
					if err == nil {
						for _, subEntry := range subEntries {
							if !subEntry.IsDir() && strings.Contains(subEntry.Name(), vid.ID) {
								fileFound = true
								break
							}
						}
					}
					if fileFound {
						break
					}
				}
			}
		}

		// If file doesn't exist but is recorded, remove the entry
		if !fileFound {
			log.Printf("Removing stale entry for video %s (file not found)", vid.ID)
			storage.RemoveDownloadedVideo(videoID, vid.ID)
		}
	}

	// Then, delete old files from disk
	return filepath.Walk(d.config.DownloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files with errors
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		baseName := filepath.Base(path)

		// Check if this file matches any of the downloaded videos for this entry
		var foundDownloadDate time.Time
		for _, vid := range videoEntry.DownloadedVideos {
			// Check if the video ID appears in the filename
			if strings.Contains(baseName, vid.ID) {
				foundDownloadDate = vid.DownloadDate
				break
			}
		}

		// Check if file is older than cutoff
		var timeToCheck time.Time
		if !foundDownloadDate.IsZero() {
			timeToCheck = foundDownloadDate
		} else {
			// Fallback to file modification time if we couldn't find in storage
			timeToCheck = info.ModTime()
		}

		if timeToCheck.Before(cutoffTime) {
			log.Printf("Removing old video: %s (download date: %s)", path, timeToCheck)
			if err := os.Remove(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			}
		}

		return nil
	})
}
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

// VideoMetadata holds metadata about a video for NFO generation
type VideoMetadata struct {
	ID          string
	Title       string
	Description string
	Uploader    string
	UploadDate  string // YYYY-MM-DD
	Duration    int    // seconds
	Thumbnail   string
}

// fetchVideoMetadata fetches video metadata using yt-dlp
func (d *Downloader) fetchVideoMetadata(videoURL string) (*VideoMetadata, error) {
	cmd := d.buildYtDlpCommand(
		"--dump-json",
		"--no-warnings",
		"--skip-download",
		videoURL,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %v", err)
	}

	metadata := &VideoMetadata{
		ID:          fmt.Sprintf("%v", data["id"]),
		Title:       fmt.Sprintf("%v", data["title"]),
		Description: fmt.Sprintf("%v", data["description"]),
		Uploader:    fmt.Sprintf("%v", data["uploader"]),
	}

	if uploadDate, ok := data["upload_date"].(string); ok && len(uploadDate) >= 8 {
		// Convert YYYYMMDD to YYYY-MM-DD
		metadata.UploadDate = uploadDate[:4] + "-" + uploadDate[4:6] + "-" + uploadDate[6:8]
	}

	if duration, ok := data["duration"].(float64); ok {
		metadata.Duration = int(duration)
	}

	if thumbnail, ok := data["thumbnail"].(string); ok {
		metadata.Thumbnail = thumbnail
	}

	return metadata, nil
}

// generateNFOFile creates an NFO XML file for a video
func (d *Downloader) generateNFOFile(channelDir string, metadata *VideoMetadata) error {
	// Find the video file matching this metadata ID
	files, err := filepath.Glob(filepath.Join(channelDir, "*"+metadata.ID+"*"))
	if err != nil || len(files) == 0 {
		return fmt.Errorf("could not find downloaded video file for %s", metadata.ID)
	}

	videoFile := files[0]
	nfoPath := strings.TrimSuffix(videoFile, filepath.Ext(videoFile)) + ".nfo"

	// Create NFO XML content
	nfoContent := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>` + escapeXML(metadata.Title) + `</title>
  <plot>` + escapeXML(metadata.Description) + `</plot>
  <director>` + escapeXML(metadata.Uploader) + `</director>
  <actor>
    <name>` + escapeXML(metadata.Uploader) + `</name>
  </actor>
  <credits>` + escapeXML(metadata.Uploader) + `</credits>
  <year>` + strings.Split(metadata.UploadDate, "-")[0] + `</year>
  <premiered>` + metadata.UploadDate + `</premiered>
  <aired>` + metadata.UploadDate + `</aired>
  <runtime>` + fmt.Sprintf("%d", metadata.Duration/60) + `</runtime>
  <uniqueid type="youtube">` + metadata.ID + `</uniqueid>
</movie>`

	if err := os.WriteFile(nfoPath, []byte(nfoContent), 0644); err != nil {
		return fmt.Errorf("failed to write NFO file: %v", err)
	}

	log.Printf("Generated NFO file: %s", nfoPath)
	return nil
}

// escapeXML escapes special XML characters
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}
