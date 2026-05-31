package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
)

func sanitizeScopeValue(v string) string {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.ReplaceAll(trimmed, "]", "")
	trimmed = strings.ReplaceAll(trimmed, "\n", " ")
	trimmed = strings.ReplaceAll(trimmed, "\r", " ")
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func logScopef(scopeType, scopeID, scopeName, format string, args ...interface{}) {
	prefix := fmt.Sprintf("[scope:%s:%s:%s]", sanitizeScopeValue(scopeType), sanitizeScopeValue(scopeID), sanitizeScopeValue(scopeName))
	log.Printf(prefix+" "+format, args...)
}

// RunScheduler continuously checks for new videos and manages downloads
func RunScheduler(ctx context.Context, config *Config, storage *Storage) {
	downloader := NewDownloader(config)
	var ticker *time.Ticker
	var lastInterval time.Duration

	// Initialize ticker with current interval
	currentInterval := config.GetCheckInterval()
	ticker = time.NewTicker(currentInterval)
	lastInterval = currentInterval
	defer ticker.Stop()

	log.Printf("Scheduler started, check interval: %v", currentInterval)

	// Run initial check immediately
	checkAndDownload(ctx, config, storage, downloader)

	for {
		select {
		case <-ctx.Done():
			log.Println("Scheduler stopping...")
			return
		case <-ticker.C:
			// Check if interval has changed
			currentInterval = config.GetCheckInterval()
			if currentInterval != lastInterval {
				log.Printf("Check interval changed from %v to %v, restarting ticker", lastInterval, currentInterval)
				ticker.Stop()
				ticker = time.NewTicker(currentInterval)
				lastInterval = currentInterval
			}

			checkAndDownload(ctx, config, storage, downloader)
		}
	}
}

// checkAndDownload performs the main work of checking and downloading videos
func checkAndDownload(ctx context.Context, config *Config, storage *Storage, downloader *Downloader) {
	log.Println("Starting scheduled check for new videos...")

	// Track if we're shutting down
	var shutdownMu sync.RWMutex
	shuttingDown := false

	// Create a semaphore to limit concurrent operations
	semaphore := make(chan struct{}, config.MaxConcurrent)
	var wg sync.WaitGroup

	// Monitor for shutdown signal
	go func() {
		<-ctx.Done()
		shutdownMu.Lock()
		shuttingDown = true
		shutdownMu.Unlock()
		log.Println("Shutdown signal received, finishing in-progress operations...")
	}()

	// Get all channels and videos upfront
	channels := storage.GetChannels()
	videos := storage.GetVideos()

	// Check channels in parallel
	for _, channel := range channels {
		shutdownMu.RLock()
		if shuttingDown {
			shutdownMu.RUnlock()
			log.Println("Skipping remaining channels due to shutdown")
			break
		}
		shutdownMu.RUnlock()

		wg.Add(1)
		go func(ch Channel) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			if err := processChannel(ctx, ch, config, storage, downloader); err != nil {
				storage.SetChannelError(ch.ID, err.Error())
			} else {
				storage.ClearChannelError(ch.ID)
			}
		}(channel)
	}

	// Check individual videos in parallel
	for _, video := range videos {
		shutdownMu.RLock()
		if shuttingDown {
			shutdownMu.RUnlock()
			log.Println("Skipping remaining videos due to shutdown")
			break
		}
		shutdownMu.RUnlock()

		wg.Add(1)
		go func(vid Video) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			if err := processVideo(ctx, vid, config, storage, downloader); err != nil {
				storage.SetVideoError(vid.ID, err.Error())
			} else {
				storage.ClearVideoError(vid.ID)
			}
		}(video)
	}

	// Wait for all checks and downloads to complete
	log.Println("Waiting for in-progress operations to complete...")
	wg.Wait()
	log.Println("All operations completed")

	// Only run cleanup if not shutting down
	shutdownMu.RLock()
	if !shuttingDown {
		shutdownMu.RUnlock()
		cleanupOldVideos(ctx, channels, videos, downloader, storage)
	} else {
		shutdownMu.RUnlock()
		log.Println("Skipping cleanup due to shutdown")
	}

	log.Println("Scheduled check completed")
}

// cleanupOldVideos runs retention cleanup for all channels and videos in parallel
func cleanupOldVideos(ctx context.Context, channels []Channel, videos []Video, downloader *Downloader, storage *Storage) {
	downloader.config.RLock()
	downloadDir := downloader.config.DownloadDir
	downloader.config.RUnlock()
	if err := storage.ReconcileDownloadedVideos(downloadDir); err != nil {
		log.Printf("Error reconciling downloaded video entries: %v", err)
	}

	if configPruningDisabled(downloader) {
		return
	}

	// Semaphore to limit concurrent cleanup operations
	cleanupSemaphore := make(chan struct{}, 2) // Allow 2 concurrent cleanups
	var wg sync.WaitGroup
	defaultRetention := getDefaultRetentionDays(downloader)

	// Clean channel videos in parallel
	for _, channel := range channels {
		// Check shutdown before launching
		select {
		case <-ctx.Done():
			log.Println("Shutdown signal during cleanup, aborting remaining cleanups")
			goto waitForCleanup
		default:
		}

		wg.Add(1)
		go func(ch Channel) {
			defer wg.Done()

			if ch.DisablePruning {
				return
			}

			retentionDays := EffectiveRetentionDays(ch.RetentionDays, defaultRetention)
			if retentionDays <= 0 {
				return
			}

			cleanupSemaphore <- struct{}{}
			defer func() { <-cleanupSemaphore }()

			if err := downloader.CleanOldVideosForChannel(ch.Name, ch.ID, retentionDays, ch.CutoffDate, storage); err != nil {
				logScopef("channel", ch.ID, ch.Name, "Error cleaning old videos for channel %s: %v", ch.Name, err)
			}
		}(channel)
	}

	// Clean individual video downloads in parallel
	for _, vid := range videos {
		// Check shutdown before launching
		select {
		case <-ctx.Done():
			log.Println("Shutdown signal during cleanup, aborting remaining cleanups")
			goto waitForCleanup
		default:
		}

		wg.Add(1)
		go func(video Video) {
			defer wg.Done()

			if video.DisablePruning {
				return
			}

			retentionDays := EffectiveRetentionDays(video.RetentionDays, defaultRetention)
			if retentionDays <= 0 {
				return
			}

			cleanupSemaphore <- struct{}{}
			defer func() { <-cleanupSemaphore }()

			removed, err := downloader.CleanOldVideosForVideo(video.Title, video.ID, retentionDays, storage)
			if err != nil {
				logScopef("video", video.ID, video.Title, "Error cleaning old videos for video %s: %v", video.Title, err)
				return
			}

			if removed {
				if err := storage.RemoveVideo(video.ID); err != nil {
					logScopef("video", video.ID, video.Title, "Error removing pruned video entry %s: %v", video.Title, err)
				}
			}
		}(vid)
	}

waitForCleanup:
	// Wait for all cleanup operations to complete
	wg.Wait()
}

// processChannel checks a channel for new videos and downloads them
func processChannel(ctx context.Context, channel Channel, config *Config, storage *Storage, downloader *Downloader) (err error) {
	effectiveRetention := EffectiveRetentionDays(channel.RetentionDays, getDefaultRetentionDays(downloader))
	logScopef("channel", channel.ID, channel.Name, "Processing channel: %s (retention: %d days)", channel.Name, effectiveRetention)

	downloadCount := 0
	skippedCount := 0
	failedDownloadCount := 0
	var firstDownloadErr error

	defer func() {
		if err != nil {
			logScopef("channel", channel.ID, channel.Name, "Finished channel %s with error: %v (downloaded=%d, skipped=%d, failed=%d)", channel.Name, err, downloadCount, skippedCount, failedDownloadCount)
			return
		}

		logScopef("channel", channel.ID, channel.Name, "Finished channel %s (downloaded=%d, skipped=%d, failed=%d)", channel.Name, downloadCount, skippedCount, failedDownloadCount)
	}()

	if !storage.HasChannel(channel.ID) {
		return nil
	}

	// Always update last checked time when we attempt to process (but not on shutdown)
	defer func() {
		if ctx.Err() != nil {
			return
		}
		if err := storage.UpdateChannelLastChecked(channel.ID, time.Now()); err != nil {
			logScopef("channel", channel.ID, channel.Name, "Failed to update channel last checked time: %v", err)
		}
	}()

	// Channel discovery uses cutoff date when configured (backlog-friendly).
	// If no cutoff is set, retention threshold is used as discovery window.
	since := BuildChannelSinceTime(time.Now(), effectiveRetention, channel.CutoffDate)
	if since.IsZero() {
		logScopef("channel", channel.ID, channel.Name, "Checking channel feed for new videos (since: none)")
	} else {
		logScopef("channel", channel.ID, channel.Name, "Checking channel feed for new videos (since: %s)", since.Format(time.RFC3339))
	}

	// Always try fast index (RSS) first, then fall back to yt-dlp
	var videos []VideoInfo
	feedSource := "rss"
	videos, err = downloader.GetChannelVideosFromRSS(channel.ID, channel.URL, since)
	if err != nil {
		logScopef("channel", channel.ID, channel.Name, "RSS feed lookup failed, falling back to yt-dlp: %v", err)
		feedSource = "yt-dlp"
		videos, err = downloader.GetChannelVideos(channel.URL, since)
	}

	if err != nil {
		return err
	}

	logScopef("channel", channel.ID, channel.Name, "Feed check complete via %s: discovered %d candidate videos", feedSource, len(videos))

	// Filter videos to only those not already downloaded
	var videosToDownload []VideoInfo
	for _, video := range videos {
		if !storage.IsVideoDownloaded(channel.ID, video.ID) {
			videosToDownload = append(videosToDownload, video)
		} else {
			skippedCount++
		}
	}

	logScopef("channel", channel.ID, channel.Name, "Eligibility result: %d to download, %d already tracked/skipped", len(videosToDownload), skippedCount)

	// If no videos need downloading, return early without creating directory
	if len(videosToDownload) == 0 {
		return nil
	}

	// Download each video that hasn't been downloaded yet
	for _, video := range videosToDownload {
		if !storage.HasChannel(channel.ID) {
			return nil
		}

		// Check if we should start a new download
		select {
		case <-ctx.Done():
			return nil // Return nil to not count as error
		default:
		}

		// Download the video
		logScopef("channel", channel.ID, channel.Name, "Attempting download for video %s (%s)", video.ID, video.Title)
		videoURL := normalizeChannelVideoURL(video.ID)
		result, err := downloader.DownloadVideo(videoURL, video.ID, channel.Name, channel.VideoQuality, channel.VideoFormat, channel.DownloadShorts)
		if err != nil {
			logScopef("channel", channel.ID, channel.Name, "Download failed for video %s (%s): %v", video.ID, video.Title, err)
			failedDownloadCount++
			if firstDownloadErr == nil {
				firstDownloadErr = err
			}
			// Continue with other videos even if one fails
		} else if result != nil && result.Skipped {
			logScopef("channel", channel.ID, channel.Name, "Skipped video %s (%s): %s", video.ID, video.Title, result.SkipReason)
			skippedCount++
		} else {
			// Mark as downloaded
			if err := storage.MarkVideoAsDownloaded(channel.ID, video.ID, video.Title, video.PublishTime); err != nil {
				logScopef("channel", channel.ID, channel.Name, "Failed to mark video as downloaded: %v", err)
			}
			logScopef("channel", channel.ID, channel.Name, "Downloaded video %s (%s)", video.ID, video.Title)
			downloadCount++
		}
	}

	if failedDownloadCount > 0 {
		return fmt.Errorf("failed to download %d video(s) for channel %s; first error: %w", failedDownloadCount, channel.Name, firstDownloadErr)
	}

	return nil
}

func normalizeChannelVideoURL(videoIDOrURL string) string {
	trimmed := strings.TrimSpace(videoIDOrURL)
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}

	return "https://www.youtube.com/watch?v=" + trimmed
}

func extractYouTubeVideoID(videoIDOrURL string) string {
	trimmed := strings.TrimSpace(videoIDOrURL)
	if trimmed == "" {
		return ""
	}

	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}

	host := strings.ToLower(parsed.Host)
	host = strings.TrimPrefix(host, "www.")

	switch host {
	case "youtu.be":
		id := strings.Trim(parsed.Path, "/")
		if id != "" {
			return id
		}
	case "youtube.com", "m.youtube.com", "youtube-nocookie.com":
		if id := strings.TrimSpace(parsed.Query().Get("v")); id != "" {
			return id
		}

		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 2 {
			switch parts[0] {
			case "shorts", "embed", "live":
				return strings.TrimSpace(parts[1])
			}
		}
	}

	return ""
}

func configPruningDisabled(d *Downloader) bool {
	d.config.RLock()
	defer d.config.RUnlock()
	return d.config.DisablePruning
}

func getDefaultRetentionDays(d *Downloader) int {
	d.config.RLock()
	defer d.config.RUnlock()
	return d.config.RetentionDays
}

// processVideo checks and downloads a specific video if not already present
func processVideo(ctx context.Context, video Video, config *Config, storage *Storage, downloader *Downloader) error {

	// Check if we should proceed before starting work
	select {
	case <-ctx.Done():
		return nil // Return nil to not count as error
	default:
	}

	if !storage.HasVideo(video.ID) {
		return nil
	}

	// Skip processing if already downloaded – no need to re-query API
	if len(video.DownloadedVideos) > 0 {
		return nil
	}

	// Always update last checked time when we attempt to process (but not on shutdown)
	defer func() {
		if ctx.Err() != nil {
			return
		}
		_ = storage.UpdateVideoLastChecked(video.ID, time.Now())
	}()

	// Try to use cached uploader info to avoid API call
	var channelName string
	var publishTime time.Time
	if video.Uploader != "" {
		channelName = strings.TrimSpace(video.Uploader)
	}
	if channelName == "" && video.UploaderID != "" {
		channelName = strings.TrimSpace(video.UploaderID)
	}

	// Only query API if we don't have cached uploader info
	if channelName == "" {
		videoInfo, liteErr := downloader.GetVideoInfoLite(video.URL)
		if liteErr == nil {
			channelName = strings.TrimSpace(videoInfo.Uploader)
			if channelName == "" {
				channelName = strings.TrimSpace(videoInfo.UploaderID)
			}
			video.Uploader = strings.TrimSpace(videoInfo.Uploader)
			video.UploaderID = strings.TrimSpace(videoInfo.UploaderID)
		}
	}

	// Only query yt-dlp if lightweight metadata path cannot provide uploader context.
	if channelName == "" {
		videoInfo, err := downloader.GetVideoInfo(video.URL)
		if err != nil {
			logScopef("video", video.ID, video.Title, "Failed to resolve channel metadata for video %s: %v", video.Title, err)
			return err
		}

		channelName = strings.TrimSpace(videoInfo.Uploader)
		if channelName == "" {
			channelName = strings.TrimSpace(videoInfo.UploaderID)
		}
		publishTime = videoInfo.PublishTime

		// Cache uploader info in memory for later persistence
		video.Uploader = strings.TrimSpace(videoInfo.Uploader)
		video.UploaderID = strings.TrimSpace(videoInfo.UploaderID)
	}

	if channelName == "" {
		logScopef("video", video.ID, video.Title, "Failed to determine channel name for video %s", video.Title)
		return fmt.Errorf("could not determine channel name for video %s", video.Title)
	}

	if !storage.HasVideo(video.ID) {
		return nil
	}

	logScopef("video", video.ID, video.Title, "Attempting download for video: %s", video.Title)

	precheckedVideoID := extractYouTubeVideoID(video.URL)
	result, err := downloader.DownloadVideo(video.URL, precheckedVideoID, channelName, video.VideoQuality, video.VideoFormat, true)
	if err != nil {
		logScopef("video", video.ID, video.Title, "Failed to download video %s: %v", video.Title, err)
		// Don't mark as downloaded - will retry on next interval
		return err
	}

	if result != nil && result.Skipped {
		if result.SkipReason != "" {
			logScopef("video", video.ID, video.Title, "Finished video %s (skipped: %s)", video.Title, result.SkipReason)
		} else {
			logScopef("video", video.ID, video.Title, "Finished video %s (skipped)", video.Title)
		}
		return nil
	}

	downloadedVideoID := strings.TrimSpace(precheckedVideoID)
	if result != nil && strings.TrimSpace(result.VideoID) != "" {
		downloadedVideoID = strings.TrimSpace(result.VideoID)
	}

	if downloadedVideoID == "" {
		logScopef("video", video.ID, video.Title, "Failed to record video %s: could not determine downloaded video ID", video.Title)
		return fmt.Errorf("could not determine downloaded video ID for %s", video.Title)
	}

	downloadedTitle := strings.TrimSpace(video.Title)
	if result != nil && strings.TrimSpace(result.VideoTitle) != "" {
		downloadedTitle = strings.TrimSpace(result.VideoTitle)
	}
	if downloadedTitle == "" {
		downloadedTitle = downloadedVideoID
	}

	// Mark as downloaded
	if err := storage.MarkVideoAsDownloaded(video.ID, downloadedVideoID, downloadedTitle, publishTime); err != nil {
		logScopef("video", video.ID, video.Title, "Failed to mark video %s as downloaded: %v", video.Title, err)
		return err
	}

	// Cache uploader info so we don't need to re-query yt-dlp on next run
	if video.Uploader != "" || video.UploaderID != "" {
		if err := storage.UpdateVideoUploaderInfo(video.ID, video.Uploader, video.UploaderID); err != nil {
			logScopef("video", video.ID, video.Title, "Failed to cache uploader info for video %s: %v", video.Title, err)
		}
	}

	logScopef("video", video.ID, video.Title, "Finished video %s (downloaded)", video.Title)

	return nil
}
