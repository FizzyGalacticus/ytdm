package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var jitterMu sync.Mutex

// RSSFeed represents a YouTube RSS feed structure
type RSSFeed struct {
	Entries []RSSEntry `xml:"entry"`
}

// RSSEntry represents a single video entry in an RSS feed
type RSSEntry struct {
	ID        string    `xml:"id"`
	Title     string    `xml:"title"`
	Published time.Time `xml:"published"`
	VideoID   string    `xml:"http://www.youtube.com/xml/schemas/2015/metadata videoId"`
}

// VideoInfo represents metadata about a video
type VideoInfo struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	UploadDate  string    `json:"upload_date"`
	Uploader    string    `json:"uploader"`
	UploaderID  string    `json:"uploader_id"`
	ChannelID   string    `json:"channel_id"`
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
	VideoID    string
	VideoTitle string
}

// NewDownloader creates a new Downloader instance
func NewDownloader(config *Config) *Downloader {
	return &Downloader{config: config}
}

// buildBaseOptions returns the common yt-dlp flags, reading all config fields under the
// config's RLock to prevent races with concurrent config updates.
func (d *Downloader) buildBaseOptions() []string {
	d.config.RLock()
	restrictFilenames := d.config.YtDlp.RestrictFilenames
	cacheDir := d.config.YtDlp.CacheDir
	throughputLimit := d.config.YtDlp.DownloadThroughputLimit
	d.config.RUnlock()

	options := []string{
		"--user-agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36",
		"--socket-timeout", "30",
		"--extractor-args", "youtube:lang=en",
		"--js-runtimes", "node",
		"--windows-filenames",
		"--quiet",
	}

	if restrictFilenames {
		options = append(options, "--restrict-filenames")
	}
	if cacheDir != "" {
		options = append(options, "--cache-dir", cacheDir)
	}
	if throughputLimit != "" {
		options = append(options, "--limit-rate", throughputLimit)
	}

	sleepInterval := d.config.GetExtractorSleepInterval()
	if sleepInterval > 0 {
		sleepSeconds := int(sleepInterval.Seconds())
		jittered := addJitterSeconds(sleepSeconds, 0.5)
		options = append(options,
			"--sleep-requests", fmt.Sprintf("%d", jittered),
			"--sleep-interval", fmt.Sprintf("%d", jittered),
			"--sleep-subtitles", fmt.Sprintf("%d", jittered),
		)
	}

	return options
}

// buildYtDlpCommand creates a yt-dlp command with cookies injected (if configured).
// All config fields are read under the config's RLock.
func (d *Downloader) buildYtDlpCommand(args ...string) *exec.Cmd {
	cmdArgs := append(d.buildBaseOptions(), args...)

	d.config.RLock()
	path := d.config.YtDlp.Path
	cookiesBrowser := d.config.YtDlp.CookiesBrowser
	cookiesFile := d.config.YtDlp.CookiesFile
	d.config.RUnlock()

	if cookiesBrowser != "" {
		cmdArgs = append([]string{"--cookies-from-browser", cookiesBrowser}, cmdArgs...)
	} else if cookiesFile != "" {
		cmdArgs = append([]string{"--cookies", cookiesFile}, cmdArgs...)
	}

	return exec.Command(path, cmdArgs...)
}

// buildYtDlpCommandNoCookies creates a yt-dlp command without any cookie arguments.
func (d *Downloader) buildYtDlpCommandNoCookies(args ...string) *exec.Cmd {
	cmdArgs := append(d.buildBaseOptions(), args...)

	d.config.RLock()
	path := d.config.YtDlp.Path
	d.config.RUnlock()

	return exec.Command(path, cmdArgs...)
}

// isAuthError returns true when yt-dlp stderr suggests that authentication or
// cookie-based access is required. Used to decide whether to retry with cookies.
func isAuthError(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, indicator := range []string{
		"sign in",
		"login required",
		"requires authentication",
		"http error 401",
		"age-restricted",
		"age restricted",
		"members only",
		"members-only",
		"premium",
		"private video",
		"not a bot",
	} {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

// runYtDlpWithCookieRetry runs yt-dlp without cookies first. If the command fails
// with an auth-related error and cookies are configured, it retries automatically
// with cookies. Returns stdout, stderr, and the exec error from the final attempt.
func (d *Downloader) runYtDlpWithCookieRetry(args []string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	cmd := d.buildYtDlpCommandNoCookies(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		return
	}

	d.config.RLock()
	hasCookies := d.config.YtDlp.CookiesBrowser != "" || d.config.YtDlp.CookiesFile != ""
	d.config.RUnlock()

	if !hasCookies || !isAuthError(stderr.String()) {
		return
	}

	log.Printf("Auth-related error detected, retrying with cookies...")
	stdout.Reset()
	stderr.Reset()

	cmd = d.buildYtDlpCommand(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return
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
	// Use yt-dlp to get video information in JSON format
	stdout, stderr, err := d.runYtDlpWithCookieRetry([]string{
		"--dump-json",
		"--skip-download",
		"--playlist-end", "50", // Limit to recent 50 videos
		channelURL,
	})
	if err != nil {
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

	return videos, nil
}

// GetChannelVideosFromRSS fetches recent videos from a channel using YouTube's public RSS feed
// This is much faster than yt-dlp but may miss videos in edge cases
// Returns VideoInfo for videos published after 'since' time
func (d *Downloader) GetChannelVideosFromRSS(channelID, channelURL string, since time.Time) ([]VideoInfo, error) {
	resolvedChannelID, err := resolveRSSChannelID(channelID, channelURL)
	if err != nil {
		return nil, fmt.Errorf("failed to extract channel ID: %v", err)
	}

	// Construct RSS feed URL
	rssURL := fmt.Sprintf("https://www.youtube.com/feeds/videos.xml?channel_id=%s", resolvedChannelID)

	// Fetch RSS feed with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", rssURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch RSS feed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RSS feed returned status %d", resp.StatusCode)
	}

	// Parse RSS feed
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read RSS response: %v", err)
	}

	var feed RSSFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("failed to parse RSS feed: %v", err)
	}

	// Convert RSS entries to VideoInfo
	var videos []VideoInfo
	for _, entry := range feed.Entries {
		// Extract video ID from entry.ID which looks like "yt:video:VIDEO_ID"
		videoID := extractVideoIDFromRSSEntry(entry)
		if videoID == "" {
			continue
		}

		// Only include videos published after 'since' time
		if entry.Published.After(since) {
			videos = append(videos, VideoInfo{
				ID:          videoID,
				Title:       entry.Title,
				PublishTime: entry.Published,
			})
		}
	}

	return videos, nil
}

func resolveRSSChannelID(channelID, channelURL string) (string, error) {
	if strings.HasPrefix(channelID, "UC") {
		return channelID, nil
	}

	// Backward compatibility for legacy entries where ID wasn't canonical.
	return extractChannelID(channelURL)
}

// extractChannelID attempts to extract the channel ID from various YouTube channel URL formats
func extractChannelID(channelURL string) (string, error) {
	// Try to match /channel/CHANNEL_ID format
	re := regexp.MustCompile(`/channel/([A-Za-z0-9_-]+)`)
	if matches := re.FindStringSubmatch(channelURL); len(matches) > 1 {
		return matches[1], nil
	}

	// Try to match /@HANDLE format (custom URL)
	re = regexp.MustCompile(`/@([A-Za-z0-9_-]+)/?$`)
	if matches := re.FindStringSubmatch(channelURL); len(matches) > 1 {
		// RSS doesn't work with custom handles; need to resolve to channel ID
		// For now, return error - could be enhanced with a separate API call
		return "", fmt.Errorf("custom channel handle not supported for RSS (use /channel/ID format)")
	}

	return "", fmt.Errorf("could not extract channel ID from URL: %s", channelURL)
}

// extractVideoIDFromRSSEntry extracts the video ID from an RSS entry
func extractVideoIDFromRSSEntry(entry RSSEntry) string {
	// First try the dedicated videoId field
	if entry.VideoID != "" {
		return entry.VideoID
	}

	// Fall back to parsing from ID field which looks like "yt:video:dQw4w9WgXcQ"
	if entry.ID != "" {
		parts := strings.Split(entry.ID, ":")
		if len(parts) >= 3 {
			return parts[len(parts)-1]
		}
	}

	return ""
}

// GetVideoInfo retrieves metadata for a specific video
func (d *Downloader) GetVideoInfo(videoURL string) (*VideoInfo, error) {
	stdout, stderr, err := d.runYtDlpWithCookieRetry([]string{"--dump-json", videoURL})
	if err != nil {
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

// ResolveChannelID resolves a YouTube channel URL to a canonical channel ID (UC...)
// using yt-dlp metadata, falling back to URL extraction if needed.
func (d *Downloader) ResolveChannelID(channelURL string) (string, error) {
	stdout, stderr, err := d.runYtDlpWithCookieRetry([]string{
		"--dump-single-json",
		"--skip-download",
		"--playlist-end", "1",
		channelURL,
	})
	if err != nil {
		return "", fmt.Errorf("yt-dlp failed resolving channel id: %v, stderr: %s", err, stderr.String())
	}

	var info VideoInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return "", fmt.Errorf("failed to parse yt-dlp channel info: %v", err)
	}

	if strings.HasPrefix(info.ChannelID, "UC") {
		return info.ChannelID, nil
	}

	if strings.HasPrefix(info.UploaderID, "UC") {
		return info.UploaderID, nil
	}

	if extracted, err := extractChannelID(channelURL); err == nil {
		return extracted, nil
	}

	return "", fmt.Errorf("could not resolve canonical channel id from url: %s", channelURL)
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

	// Compute channel directory path (but don't create yet)
	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))

	videoID := strings.TrimSpace(expectedVideoID)
	result.VideoID = videoID

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
		"--add-metadata",
		"--write-info-json",
		"--write-thumbnail",
		"--convert-thumbnails", "jpg",
		"--print", "after_move:%(id)s\t%(title)s",
		videoURL,
	)

	// Try without cookies first; on auth errors retry with cookies if configured
	var runErr error
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	tryDownload := func(withCookies bool) {
		stdout.Reset()
		stderr.Reset()
		var cmd *exec.Cmd
		if withCookies {
			cmd = d.buildYtDlpCommand(cmdArgs...)
		} else {
			cmd = d.buildYtDlpCommandNoCookies(cmdArgs...)
		}
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr = cmd.Run()
	}

	tryDownload(false)
	if runErr != nil {
		errOutput := strings.TrimSpace(stderr.String())

		d.config.RLock()
		hasCookies := d.config.YtDlp.CookiesBrowser != "" || d.config.YtDlp.CookiesFile != ""
		d.config.RUnlock()

		if hasCookies && isAuthError(errOutput) {
			log.Printf("Auth error on download, retrying with cookies...")
			tryDownload(true)
			errOutput = strings.TrimSpace(stderr.String())
		}

		if runErr != nil {
			if isSkippableYtDlpOutput(errOutput) {
				reason := extractSkipReason(errOutput)
				result.Skipped = true
				result.SkipReason = reason
				return result, nil
			}
			return result, fmt.Errorf("yt-dlp download failed: %v, stderr: %s", runErr, errOutput)
		}
	}

	printedID, printedTitle := parseDownloadedVideoInfo(stdout.String())
	if printedID != "" {
		result.VideoID = printedID
		if videoID == "" {
			videoID = printedID
		}
	}
	if printedTitle != "" {
		result.VideoTitle = printedTitle
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
			return result, nil
		}
	}

	log.Printf("Successfully downloaded video: %s", videoURL)
	result.Downloaded = true

	if result.VideoID != "" {
		if metadata, metaErr := d.loadMetadataFromInfoJSON(channelDir, result.VideoID); metaErr != nil {
			log.Printf("Warning: failed to load info json for NFO (%s): %v", result.VideoID, metaErr)
		} else if err := d.generateNFOFile(channelDir, metadata); err != nil {
			log.Printf("Warning: failed to generate NFO file: %v", err)
		}
	}

	return result, nil
}

func parseDownloadedVideoInfo(output string) (string, string) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "\t", 2)
		id := strings.TrimSpace(parts[0])
		title := ""
		if len(parts) > 1 {
			title = strings.TrimSpace(parts[1])
		}

		if id != "" {
			return id, title
		}
	}

	return "", ""
}

func (d *Downloader) loadMetadataFromInfoJSON(channelDir, videoID string) (*VideoMetadata, error) {
	if videoID == "" {
		return nil, fmt.Errorf("empty video id")
	}

	pattern := filepath.Join(channelDir, "*"+videoID+"*.info.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to search info json: %v", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no info json file found for %s", videoID)
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		return nil, fmt.Errorf("failed to read info json: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse info json: %v", err)
	}

	metadata := &VideoMetadata{
		ID:          videoID,
		Title:       getStringField(raw, "title"),
		Description: getStringField(raw, "description"),
		Uploader:    getStringField(raw, "uploader"),
	}

	if uploadDate, ok := raw["upload_date"].(string); ok && len(uploadDate) >= 8 {
		metadata.UploadDate = uploadDate[:4] + "-" + uploadDate[4:6] + "-" + uploadDate[6:8]
	}

	if duration, ok := raw["duration"].(float64); ok {
		metadata.Duration = int(duration)
	}

	if thumbnail, ok := raw["thumbnail"].(string); ok {
		metadata.Thumbnail = strings.TrimSpace(thumbnail)
	}

	metadata.Chapters = extractVideoChapters(raw)

	if metadata.UploadDate == "" {
		metadata.UploadDate = time.Now().Format("2006-01-02")
	}
	if metadata.Title == "" {
		metadata.Title = videoID
	}
	if metadata.Uploader == "" {
		metadata.Uploader = "unknown"
	}

	return metadata, nil
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
		lowerName := strings.ToLower(name)
		if strings.Contains(name, videoID) &&
			!strings.HasSuffix(lowerName, ".nfo") &&
			!strings.HasSuffix(lowerName, ".info.json") &&
			!strings.HasSuffix(lowerName, ".jpg") &&
			!strings.HasSuffix(lowerName, ".jpeg") &&
			!strings.HasSuffix(lowerName, ".png") &&
			!strings.HasSuffix(lowerName, ".webp") {
			count++
		}
	}

	return count
}

func (d *Downloader) deleteInfoJSONFilesForVideo(channelDir, videoID string) error {
	if videoID == "" {
		return fmt.Errorf("empty video id")
	}

	pattern := filepath.Join(channelDir, "*"+videoID+"*.info.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to search info json: %v", err)
	}

	var firstErr error
	for _, file := range files {
		if rmErr := os.Remove(file); rmErr != nil {
			if firstErr == nil {
				firstErr = rmErr
			}
			continue
		}
		log.Printf("Deleted metadata JSON: %s", file)
	}

	return firstErr
}

// MigrateUnknownVideos relocates files from the legacy "unknown" folder into
// channel-named folders using uploader metadata from existing info.json files.
func (d *Downloader) MigrateUnknownVideos() (migratedVideos int, movedFiles int, errors []string) {
	unknownDir := filepath.Join(d.config.DownloadDir, sanitizeFilename("unknown"))
	if stat, err := os.Stat(unknownDir); err != nil || !stat.IsDir() {
		return 0, 0, nil
	}

	infoFiles, err := filepath.Glob(filepath.Join(unknownDir, "*.info.json"))
	if err != nil {
		return 0, 0, []string{fmt.Sprintf("failed to list info json files in %s: %v", unknownDir, err)}
	}

	processedVideoIDs := map[string]struct{}{}
	for _, infoPath := range infoFiles {
		meta, metaErr := d.loadVideoMetadataFromInfoJSONFile(infoPath)
		if metaErr != nil {
			errors = append(errors, fmt.Sprintf("failed reading %s: %v", infoPath, metaErr))
			continue
		}

		videoID := strings.TrimSpace(meta.ID)
		if videoID == "" {
			errors = append(errors, fmt.Sprintf("missing video id in %s", infoPath))
			continue
		}

		if _, seen := processedVideoIDs[videoID]; seen {
			continue
		}
		processedVideoIDs[videoID] = struct{}{}

		channelName := strings.TrimSpace(meta.Uploader)
		if channelName == "" || strings.EqualFold(channelName, "unknown") {
			errors = append(errors, fmt.Sprintf("missing uploader metadata for video %s in %s", videoID, infoPath))
			continue
		}

		targetDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
		if targetDir == unknownDir {
			continue
		}

		if mkErr := os.MkdirAll(targetDir, 0755); mkErr != nil {
			errors = append(errors, fmt.Sprintf("failed to create target dir %s for %s: %v", targetDir, videoID, mkErr))
			continue
		}

		movedForVideo, moveErr := moveFilesForVideoID(unknownDir, targetDir, videoID)
		if moveErr != nil {
			errors = append(errors, fmt.Sprintf("failed moving files for %s: %v", videoID, moveErr))
			continue
		}

		if movedForVideo > 0 {
			migratedVideos++
			movedFiles += movedForVideo
		}
	}

	return migratedVideos, movedFiles, errors
}

func (d *Downloader) loadVideoMetadataFromInfoJSONFile(infoPath string) (*VideoMetadata, error) {
	data, err := os.ReadFile(infoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read info json: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse info json: %v", err)
	}

	metadata := &VideoMetadata{
		ID:          getStringField(raw, "id"),
		Title:       getStringField(raw, "title"),
		Description: getStringField(raw, "description"),
		Uploader:    getStringField(raw, "uploader"),
	}

	if metadata.Uploader == "" {
		metadata.Uploader = getStringField(raw, "channel")
	}
	if metadata.Uploader == "" {
		metadata.Uploader = getStringField(raw, "uploader_id")
	}

	if uploadDate, ok := raw["upload_date"].(string); ok && len(uploadDate) >= 8 {
		metadata.UploadDate = uploadDate[:4] + "-" + uploadDate[4:6] + "-" + uploadDate[6:8]
	}

	if duration, ok := raw["duration"].(float64); ok {
		metadata.Duration = int(duration)
	}

	if thumbnail, ok := raw["thumbnail"].(string); ok {
		metadata.Thumbnail = strings.TrimSpace(thumbnail)
	}

	metadata.Chapters = extractVideoChapters(raw)

	return metadata, nil
}

func moveFilesForVideoID(fromDir, toDir, videoID string) (int, error) {
	if strings.TrimSpace(videoID) == "" {
		return 0, fmt.Errorf("empty video id")
	}

	entries, err := os.ReadDir(fromDir)
	if err != nil {
		return 0, err
	}

	moved := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.Contains(name, videoID) {
			continue
		}

		src := filepath.Join(fromDir, name)
		dst := filepath.Join(toDir, name)

		if _, statErr := os.Stat(dst); statErr == nil {
			return moved, fmt.Errorf("destination already exists: %s", dst)
		}

		if err := os.Rename(src, dst); err != nil {
			return moved, err
		}
		moved++
	}

	return moved, nil
}

func getStringField(raw map[string]interface{}, key string) string {
	val, ok := raw[key]
	if !ok || val == nil {
		return ""
	}

	if s, ok := val.(string); ok {
		return strings.TrimSpace(s)
	}

	str := strings.TrimSpace(fmt.Sprintf("%v", val))
	if str == "<nil>" {
		return ""
	}

	return str
}

func extractVideoChapters(raw map[string]interface{}) []VideoChapter {
	chaptersRaw, ok := raw["chapters"].([]interface{})
	if !ok || len(chaptersRaw) == 0 {
		return nil
	}

	chapters := make([]VideoChapter, 0, len(chaptersRaw))
	for _, entry := range chaptersRaw {
		chapterMap, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}

		title := getStringField(chapterMap, "title")
		if title == "" {
			title = "Chapter"
		}

		var startTime float64
		if start, ok := chapterMap["start_time"].(float64); ok {
			startTime = start
		}

		var endTime float64
		if end, ok := chapterMap["end_time"].(float64); ok {
			endTime = end
		}

		chapters = append(chapters, VideoChapter{
			Title:     title,
			StartTime: startTime,
			EndTime:   endTime,
		})
	}

	if len(chapters) == 0 {
		return nil
	}

	return chapters
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

// CleanOldVideosForChannel removes channel videos that are before cutoff date or
// whose download age exceeds retention days.
func (d *Downloader) CleanOldVideosForChannel(channelName, channelID string, retentionDays int, cutoffDate time.Time, storage *Storage) error {
	if retentionDays <= 0 {
		return nil
	}

	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channelName))
	if _, err := os.Stat(channelDir); os.IsNotExist(err) {
		return nil // Channel directory doesn't exist yet
	}

	cutoffTime := retentionCutoff(time.Now(), retentionDays)

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
		trackedVideo, tracked := findTrackedVideo(baseName, trackedVideos)
		if !tracked {
			return nil
		}

		if trackedVideo.DisablePruning {
			return nil
		}

		shouldPruneByRetention := !trackedVideo.DownloadDate.IsZero() && trackedVideo.DownloadDate.Before(cutoffTime)
		shouldPruneByCutoff := shouldPruneByChannelCutoff(trackedVideo.PublishDate, cutoffDate)

		if shouldPruneByRetention || shouldPruneByCutoff {
			if err := os.Remove(path); err != nil {
				log.Printf("Failed to remove %s: %v", path, err)
			} else {
				reason := "retention"
				if shouldPruneByCutoff {
					reason = "cutoff"
				}
				log.Printf("Pruned video file: %s (reason: %s, download_date: %s, publish_date: %s)", path, reason, trackedVideo.DownloadDate, trackedVideo.PublishDate)
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

// CleanOldVideosForVideo removes standalone video files outside retention and
// reports whether the video entry should be removed permanently.
func (d *Downloader) CleanOldVideosForVideo(_ string, videoID string, retentionDays int, storage *Storage) (bool, error) {
	if retentionDays <= 0 {
		return false, nil
	}

	cutoffTime := retentionCutoff(time.Now(), retentionDays)

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
		return false, nil // Video entry not found
	}

	entryShouldBeRemoved := false

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
		trackedVideo, tracked := findTrackedVideo(baseName, trackedVideos)
		if !tracked || trackedVideo.DownloadDate.IsZero() {
			return nil
		}

		if trackedVideo.DisablePruning {
			return nil
		}

		if trackedVideo.DownloadDate.Before(cutoffTime) {
			if removeErr := os.Remove(path); removeErr != nil {
				log.Printf("Failed to remove %s: %v", path, removeErr)
			} else {
				entryShouldBeRemoved = true
				log.Printf("Pruned video file: %s (download date: %s)", path, trackedVideo.DownloadDate)
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

	if err != nil {
		return false, err
	}

	return entryShouldBeRemoved, nil
}

func shouldPruneByChannelCutoff(publishDate, cutoffDate time.Time) bool {
	if cutoffDate.IsZero() || publishDate.IsZero() {
		return false
	}

	return publishDate.Before(startOfDayUTC(cutoffDate))
}

func findTrackedVideo(baseName string, videos []DownloadedVideo) (DownloadedVideo, bool) {
	for _, vid := range videos {
		if strings.Contains(baseName, vid.ID) {
			return vid, true
		}
	}

	return DownloadedVideo{}, false
}

// RemoveChannelResources deletes all downloaded files/resources for a channel.
// It removes the channel directory and any tracked files that may exist elsewhere.
func (d *Downloader) RemoveChannelResources(channel Channel) error {
	var firstErr error

	channelDir := filepath.Join(d.config.DownloadDir, sanitizeFilename(channel.Name))
	if err := os.RemoveAll(channelDir); err != nil {
		firstErr = err
	}

	trackedIDs := map[string]struct{}{}
	for _, vid := range channel.DownloadedVideos {
		if vid.ID != "" {
			trackedIDs[vid.ID] = struct{}{}
		}
	}

	if len(trackedIDs) == 0 {
		return firstErr
	}

	_ = filepath.Walk(d.config.DownloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		for id := range trackedIDs {
			if strings.Contains(name, id) {
				if rmErr := os.Remove(path); rmErr != nil && firstErr == nil {
					firstErr = rmErr
				}
				break
			}
		}
		return nil
	})

	return firstErr
}

// RemoveVideoResources deletes all downloaded files/resources for an individual video entry.
func (d *Downloader) RemoveVideoResources(video Video) error {
	var firstErr error

	trackedIDs := map[string]struct{}{}
	if video.ID != "" {
		trackedIDs[video.ID] = struct{}{}
	}
	for _, vid := range video.DownloadedVideos {
		if vid.ID != "" {
			trackedIDs[vid.ID] = struct{}{}
		}
	}

	if len(trackedIDs) == 0 {
		return nil
	}

	_ = filepath.Walk(d.config.DownloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		for id := range trackedIDs {
			if strings.Contains(name, id) {
				if rmErr := os.Remove(path); rmErr != nil && firstErr == nil {
					firstErr = rmErr
				}
				break
			}
		}
		return nil
	})

	// Remove any empty subdirectories left behind
	_ = filepath.Walk(d.config.DownloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() || path == d.config.DownloadDir {
			return nil
		}
		entries, readErr := os.ReadDir(path)
		if readErr == nil && len(entries) == 0 {
			_ = os.Remove(path)
		}
		return nil
	})

	return firstErr
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
	Chapters    []VideoChapter
}

type VideoChapter struct {
	Title     string
	StartTime float64
	EndTime   float64
}

// fetchVideoMetadata fetches video metadata using yt-dlp
func (d *Downloader) fetchVideoMetadata(videoURL string) (*VideoMetadata, error) {
	stdout, _, err := d.runYtDlpWithCookieRetry([]string{
		"--dump-json",
		"--no-warnings",
		"--skip-download",
		videoURL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
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

	chaptersXML := ""
	if len(metadata.Chapters) > 0 {
		chapterLines := make([]string, 0, len(metadata.Chapters)+2)
		chapterLines = append(chapterLines, "  <chapters>")
		for _, chapter := range metadata.Chapters {
			chapterLines = append(chapterLines,
				"    <chapter>",
				"      <title>"+escapeXML(chapter.Title)+"</title>",
				"      <start>"+fmt.Sprintf("%.3f", chapter.StartTime)+"</start>",
				"      <end>"+fmt.Sprintf("%.3f", chapter.EndTime)+"</end>",
				"    </chapter>",
			)
		}
		chapterLines = append(chapterLines, "  </chapters>")
		chaptersXML = "\n" + strings.Join(chapterLines, "\n")
	}

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
` + chaptersXML + `
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
