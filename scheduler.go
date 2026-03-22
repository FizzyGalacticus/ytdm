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

			retentionDays := effectiveRetentionDays(ch.RetentionDays, defaultRetention)
			if retentionDays <= 0 {
				return
			}

			cleanupSemaphore <- struct{}{}
			defer func() { <-cleanupSemaphore }()

			if err := downloader.CleanOldVideosForChannel(ch.Name, ch.ID, retentionDays, storage); err != nil {
				log.Printf("Error cleaning old videos for channel %s: %v", ch.Name, err)
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

			retentionDays := effectiveRetentionDays(video.RetentionDays, defaultRetention)
			if retentionDays <= 0 {
				return
			}

			cleanupSemaphore <- struct{}{}
			defer func() { <-cleanupSemaphore }()

			if err := downloader.CleanOldVideosForVideo(video.Title, video.ID, retentionDays, storage); err != nil {
				log.Printf("Error cleaning old videos for video %s: %v", video.Title, err)
			}
		}(vid)
	}

waitForCleanup:
	// Wait for all cleanup operations to complete
	wg.Wait()
}

// processChannel checks a channel for new videos and downloads them
func processChannel(ctx context.Context, channel Channel, config *Config, storage *Storage, downloader *Downloader) (err error) {
	effectiveRetention := effectiveRetentionDays(channel.RetentionDays, getDefaultRetentionDays(downloader))
	log.Printf("Processing channel: %s (retention: %d days)", channel.Name, effectiveRetention)

	downloadCount := 0
	skippedCount := 0
	failedDownloadCount := 0
	var firstDownloadErr error

	defer func() {
		if err != nil {
			log.Printf("Finished channel %s with error: %v (downloaded=%d, skipped=%d, failed=%d)", channel.Name, err, downloadCount, skippedCount, failedDownloadCount)
			return
		}

		log.Printf("Finished channel %s (downloaded=%d, skipped=%d, failed=%d)", channel.Name, downloadCount, skippedCount, failedDownloadCount)
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
			log.Printf("Failed to update channel last checked time: %v", err)
		}
	}()

	// Only download channel videos that are new enough for the retention window.
	// If a cutoff date is also set, use the stricter of the two constraints.
	since := buildChannelSinceTime(time.Now(), effectiveRetention, channel.CutoffDate)

	// Always try fast index (RSS) first, then fall back to yt-dlp
	var videos []VideoInfo
	videos, err = downloader.GetChannelVideosFromRSS(channel.ID, channel.URL, since)
	if err != nil {
		videos, err = downloader.GetChannelVideos(channel.URL, since)
	}

	if err != nil {
		return err
	}

	// Filter videos to only those not already downloaded
	var videosToDownload []VideoInfo
	for _, video := range videos {
		if !storage.IsVideoDownloaded(channel.ID, video.ID) {
			videosToDownload = append(videosToDownload, video)
		} else {
			skippedCount++
		}
	}

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
		videoURL := normalizeChannelVideoURL(video.ID)
		result, err := downloader.DownloadVideo(videoURL, video.ID, channel.Name, channel.VideoQuality, channel.VideoFormat, channel.DownloadShorts)
		if err != nil {
			failedDownloadCount++
			if firstDownloadErr == nil {
				firstDownloadErr = err
			}
			// Continue with other videos even if one fails
		} else if result != nil && result.Skipped {
			skippedCount++
		} else {
			// Mark as downloaded
			if err := storage.MarkVideoAsDownloaded(channel.ID, video.ID, video.Title); err != nil {
				log.Printf("Failed to mark video as downloaded: %v", err)
			}
			downloadCount++
		}
	}

	if failedDownloadCount > 0 {
		return fmt.Errorf("failed to download %d video(s) for channel %s; first error: %w", failedDownloadCount, channel.Name, firstDownloadErr)
	}

	return nil
}

func effectiveRetentionDays(itemRetention, defaultRetention int) int {
	if itemRetention > 0 {
		return itemRetention
	}
	return defaultRetention
}

func buildChannelSinceTime(now time.Time, retentionDays int, cutoffDate time.Time) time.Time {
	var since time.Time

	if retentionDays > 0 {
		retentionThreshold := startOfDayUTC(now).AddDate(0, 0, -retentionDays)
		since = retentionThreshold.Add(-time.Second)
	}

	if !cutoffDate.IsZero() {
		cutoffSince := startOfDayUTC(cutoffDate).Add(-time.Second)
		if since.IsZero() || cutoffSince.After(since) {
			since = cutoffSince
		}
	}

	return since
}

func startOfDayUTC(t time.Time) time.Time {
	year, month, day := t.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
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

	// Always update last checked time when we attempt to process (but not on shutdown)
	defer func() {
		if ctx.Err() != nil {
			return
		}
		_ = storage.UpdateVideoLastChecked(video.ID, time.Now())
	}()

	precheckedVideoID := extractYouTubeVideoID(video.URL)
	if precheckedVideoID != "" && storage.IsVideoDownloaded(video.ID, precheckedVideoID) {
		return nil
	}

	// Retention is based on download date, so don't skip based on publish date

	// Download the video with the video's preferred quality and shorts settings
	channelName := "unknown"

	if !storage.HasVideo(video.ID) {
		return nil
	}

	log.Printf("Attempting download for video: %s", video.Title)

	result, err := downloader.DownloadVideo(video.URL, precheckedVideoID, channelName, video.VideoQuality, video.VideoFormat, video.DownloadShorts)
	if err != nil {
		log.Printf("Failed to download video %s: %v", video.Title, err)
		// Don't mark as downloaded - will retry on next interval
		return err
	}

	if result != nil && result.Skipped {
		if result.SkipReason != "" {
			log.Printf("Finished video %s (skipped: %s)", video.Title, result.SkipReason)
		} else {
			log.Printf("Finished video %s (skipped)", video.Title)
		}
		return nil
	}

	downloadedVideoID := strings.TrimSpace(precheckedVideoID)
	if result != nil && strings.TrimSpace(result.VideoID) != "" {
		downloadedVideoID = strings.TrimSpace(result.VideoID)
	}

	if downloadedVideoID == "" {
		log.Printf("Failed to record video %s: could not determine downloaded video ID", video.Title)
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
	if err := storage.MarkVideoAsDownloaded(video.ID, downloadedVideoID, downloadedTitle); err != nil {
		log.Printf("Failed to mark video %s as downloaded: %v", video.Title, err)
		return err
	}

	log.Printf("Finished video %s (downloaded)", video.Title)

	return nil
}
