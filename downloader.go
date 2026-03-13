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

// DownloadResult captures the outcome of a download attempt.
type DownloadResult struct {
	Downloaded bool
	Skipped    bool
	SkipReason string
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
		"--js-runtimes", "node", // Use node for JavaScript extraction (more reliable than deno)
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

// buildFormatString constructs a yt-dlp format string based on desired quality and format
// quality can be "best", a specific height like "720", "480", "360", or empty for defaults
// format can be "mp4", "webm", "mkv", or empty for any format (defaults to mp4)
func (d *Downloader) buildFormatString(quality, format string) string {
	quality = strings.TrimSpace(quality)
	format = strings.TrimSpace(format)

	// Default to mp4 if no format specified
	if format == "" {
		format = "mp4"
	}

	if quality == "" || quality == "best" {
		// Prefer video in specified format, but don't filter audio by extension
		// Audio streams may not be available in the target container format
		return fmt.Sprintf("bestvideo[ext=%s]+bestaudio/bestvideo+bestaudio/best", format)
	}

	// For specific quality heights, prefer video in specified format with any audio
	// The merge-output-format option will handle container conversion
	return fmt.Sprintf("bestvideo[height<=%s][ext=%s]+bestaudio/bestvideo[height<=%s]+bestaudio/best", quality, format, quality)
}

// DownloadVideo downloads a video to the specified directory.
// expectedVideoID should be provided when known so we can reliably detect whether
// a file was actually created. If empty, metadata ID is used when available.
func (d *Downloader) DownloadVideo(videoURL, expectedVideoID, channelName, quality, format string, downloadShorts bool) (*DownloadResult, error) {
	result := &DownloadResult{}

	// Create channel subdirectory
	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		return result, fmt.Errorf("failed to create directory: %v", err)
	}

	log.Printf("Downloading video: %s to %s (quality: %s, format: %s, downloadShorts: %v)", videoURL, channelDir, quality, format, downloadShorts)

	// First, fetch metadata with yt-dlp
	metadata, err := d.fetchVideoMetadata(videoURL)
	if err != nil {
		log.Printf("Warning: could not fetch metadata for %s: %v", videoURL, err)
		metadata = nil // Continue with download even if metadata fails
	}

	videoID := strings.TrimSpace(expectedVideoID)
	if videoID == "" && metadata != nil {
		videoID = strings.TrimSpace(metadata.ID)
	}

	beforeCount := -1
	if videoID != "" {
		beforeCount = d.countVideoFiles(channelDir, videoID)
	}

	// Build format string based on desired quality and format
	formatStr := d.buildFormatString(quality, format)

	// Build match filters based on shorts preference
	var matchFilter string
	if downloadShorts {
		// Allow shorts: only exclude live streams and very short videos
		matchFilter = "!is_live & duration>60"
	} else {
		// Exclude shorts: exclude live streams, short videos, and vertical aspect ratio
		// Use aspect_ratio field which is a proper float comparison
		matchFilter = "!is_live & duration>60 & aspect_ratio>=1"
	}

	// Build yt-dlp command for download
	cmdArgs := []string{
		"-o", filepath.Join(channelDir, d.config.FileNamePattern),
		"--no-playlist",
		"-f", formatStr,
	}

	// Only add merge-output-format if a format was specified
	normalizedFormat := strings.TrimSpace(format)
	if normalizedFormat == "" {
		normalizedFormat = "mp4"
	}
	cmdArgs = append(cmdArgs, "--merge-output-format", normalizedFormat)

	cmdArgs = append(cmdArgs,
		"--match-filters", matchFilter,
		"--embed-chapters",
		videoURL,
	)

	cmd := d.buildYtDlpCommand(cmdArgs...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if isSkippableYtDlpOutput(errOutput) {
			reason := extractSkipReason(errOutput)
			result.Skipped = true
			result.SkipReason = reason
			log.Printf("Skipped video %s: %s", videoURL, reason)
			return result, nil
		}
		return result, fmt.Errorf("yt-dlp download failed: %v, stderr: %s", err, errOutput)
	}

	if videoID != "" {
		afterCount := d.countVideoFiles(channelDir, videoID)
		if beforeCount >= 0 && afterCount <= beforeCount {
			reason := extractSkipReason(strings.TrimSpace(stderr.String()))
			if reason == "" {
				reason = "no downloadable file was produced"
			}
			result.Skipped = true
			result.SkipReason = reason
			log.Printf("Skipped video %s (%s): %s", videoURL, videoID, reason)
			return result, nil
		}
	}

	log.Printf("Successfully downloaded video: %s", videoURL)
	result.Downloaded = true

	// Generate NFO file if metadata was available
	if metadata != nil {
		if err := d.generateNFOFile(channelDir, metadata); err != nil {
			log.Printf("Warning: failed to generate NFO file: %v", err)
		}
	}

	return result, nil
}

func (d *Downloader) countVideoFiles(channelDir, videoID string) int {
	if videoID == "" {
		return 0
	}

	entries, err := os.ReadDir(channelDir)
	if err != nil {
		return 0
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.Contains(name, videoID) && !strings.HasSuffix(strings.ToLower(name), ".nfo") {
			count++
		}
	}

	return count
}

func isSkippableYtDlpOutput(output string) bool {
	if output == "" {
		return false
	}

	lower := strings.ToLower(output)
	skippableSignals := []string{
		"does not pass filter",
		"video unavailable",
		"this video is unavailable",
		"private video",
		"members-only",
		"requested format is not available",
		"no video formats found",
	}

	for _, sig := range skippableSignals {
		if strings.Contains(lower, sig) {
			return true
		}
	}

	return false
}

func extractSkipReason(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "filtered out or unavailable"
	}

	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "does not pass filter") ||
			strings.Contains(lower, "unavailable") ||
			strings.Contains(lower, "private") ||
			strings.Contains(lower, "members-only") ||
			strings.Contains(lower, "no video formats") ||
			strings.Contains(lower, "requested format") {
			return line
		}
	}

	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}

	return "filtered out or unavailable"
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

	trackedVideos := channelData.DownloadedVideos

	// Then, delete old tracked files from disk
	err := filepath.Walk(channelDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files with errors
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		baseName := filepath.Base(path)
		foundDownloadDate, tracked := findTrackedDownloadDate(baseName, trackedVideos)
		if !tracked || foundDownloadDate.IsZero() {
			return nil
		}

		if foundDownloadDate.Before(cutoffTime) {
			log.Printf("Removing old tracked video: %s (download date: %s)", path, foundDownloadDate)
			if err := os.Remove(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			}
		}

		return nil
	})

	// After cleanup, check if directory is empty and remove it
	if err == nil {
		entries, readErr := os.ReadDir(channelDir)
		if readErr == nil && len(entries) == 0 {
			log.Printf("Removing empty channel directory: %s", channelDir)
			if rmErr := os.Remove(channelDir); rmErr != nil {
				log.Printf("Failed to remove empty directory %s: %v", channelDir, rmErr)
			}
		}
	}

	return err
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

	trackedVideos := videoEntry.DownloadedVideos

	// Then, delete old tracked files from disk
	err := filepath.Walk(d.config.DownloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files with errors
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		baseName := filepath.Base(path)
		foundDownloadDate, tracked := findTrackedDownloadDate(baseName, trackedVideos)
		if !tracked || foundDownloadDate.IsZero() {
			return nil
		}

		if foundDownloadDate.Before(cutoffTime) {
			log.Printf("Removing old tracked video: %s (download date: %s)", path, foundDownloadDate)
			if removeErr := os.Remove(path); removeErr != nil {
				log.Printf("Failed to remove %s: %v", path, removeErr)
			} else {
				// Check if parent directory is now empty and remove it
				parentDir := filepath.Dir(path)
				if parentDir != d.config.DownloadDir {
					if entries, readErr := os.ReadDir(parentDir); readErr == nil && len(entries) == 0 {
						log.Printf("Removing empty directory: %s", parentDir)
						if rmErr := os.Remove(parentDir); rmErr != nil {
							log.Printf("Failed to remove empty directory %s: %v", parentDir, rmErr)
						}
					}
				}
			}
		}

		return nil
	})

	return err
}

func findTrackedDownloadDate(baseName string, videos []DownloadedVideo) (time.Time, bool) {
	for _, vid := range videos {
		if strings.Contains(baseName, vid.ID) {
			return vid.DownloadDate, true
		}
	}

	return time.Time{}, false
}

// sanitizeFilename removes or replaces characters that are invalid in filenames
// Handles characters that are problematic on Windows, Linux, macOS, and network filesystems
func sanitizeFilename(name string) string {
	// Replace invalid characters with underscores
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|", "\x00"}
	result := name
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "_")
	}

	// Remove control characters (ASCII 0-31 and 127)
	for i := 0; i < 32; i++ {
		result = strings.ReplaceAll(result, string(rune(i)), "")
	}
	result = strings.ReplaceAll(result, string(rune(127)), "")

	// Trim leading/trailing whitespace and dots (problematic on Windows)
	result = strings.Trim(result, " .")

	// Ensure we don't end up with an empty string
	if result == "" {
		result = "unnamed"
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
