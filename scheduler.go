package main

import (
	"context"
	"fmt"
	"log"
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
				log.Printf("Error processing channel %s: %v", ch.Name, err)
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
				log.Printf("Error processing video %s: %v", vid.Title, err)
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
	log.Println("Starting cleanup of old videos...")

	// Semaphore to limit concurrent cleanup operations
	cleanupSemaphore := make(chan struct{}, 2) // Allow 2 concurrent cleanups
	var wg sync.WaitGroup

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

			cleanupSemaphore <- struct{}{}
			defer func() { <-cleanupSemaphore }()

			if err := downloader.CleanOldVideosForChannel(ch.Name, ch.ID, ch.RetentionDays, storage); err != nil {
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

			cleanupSemaphore <- struct{}{}
			defer func() { <-cleanupSemaphore }()

			if err := downloader.CleanOldVideosForVideo(video.Title, video.ID, video.RetentionDays, storage); err != nil {
				log.Printf("Error cleaning old videos for video %s: %v", video.Title, err)
			}
		}(vid)
	}

waitForCleanup:
	// Wait for all cleanup operations to complete
	wg.Wait()
	log.Println("Cleanup of old videos completed")
}

// processChannel checks a channel for new videos and downloads them
func processChannel(ctx context.Context, channel Channel, config *Config, storage *Storage, downloader *Downloader) error {
	log.Printf("Processing channel: %s (retention: %d days)", channel.Name, channel.RetentionDays)

	if !storage.HasChannel(channel.ID) {
		log.Printf("Channel %s removed during processing; skipping", channel.Name)
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

	// Determine the time window to check for videos
	// Retention is based on download date, so don't filter by publish date unless a cutoff is set.
	var since time.Time
	if !channel.CutoffDate.IsZero() {
		log.Printf("Applying cutoff date for channel %s: %s (inclusive)", channel.Name, channel.CutoffDate.Format("2006-01-02"))
		// Subtract one day to include videos published on the cutoff date
		since = channel.CutoffDate.AddDate(0, 0, -1)
	}

	// Always try fast index (RSS) first, then fall back to yt-dlp
	var videos []VideoInfo
	var err error
	videos, err = downloader.GetChannelVideosFromRSS(channel.ID, channel.URL, since)
	if err != nil {
		log.Printf("Fast index (RSS) failed for channel %s, falling back to yt-dlp: %v", channel.Name, err)
		videos, err = downloader.GetChannelVideos(channel.URL, since)
	}

	if err != nil {
		return err
	}

	downloadCount := 0
	skippedAlreadyDownloadedCount := 0
	skippedByDownloaderCount := 0
	failedDownloadCount := 0
	var firstDownloadErr error

	// Download each video that hasn't been downloaded yet
	for _, video := range videos {
		if !storage.HasChannel(channel.ID) {
			log.Printf("Channel %s removed during processing; stopping remaining downloads", channel.Name)
			return nil
		}

		// Check if we should start a new download
		select {
		case <-ctx.Done():
			log.Printf("Shutdown signal received, skipping remaining videos for channel %s", channel.Name)
			return nil // Return nil to not count as error
		default:
		}

		// Skip if already downloaded
		if storage.IsVideoDownloaded(channel.ID, video.ID) {
			skippedAlreadyDownloadedCount++
			continue
		}

		// Download the video
		result, err := downloader.DownloadVideo(video.ID, video.ID, channel.Name, channel.VideoQuality, channel.VideoFormat, channel.DownloadShorts)
		if err != nil {
			log.Printf("Failed to download video %s: %v", video.Title, err)
			failedDownloadCount++
			if firstDownloadErr == nil {
				firstDownloadErr = err
			}
			// Continue with other videos even if one fails
		} else if result != nil && result.Skipped {
			skippedByDownloaderCount++
		} else {
			// Mark as downloaded
			if err := storage.MarkVideoAsDownloaded(channel.ID, video.ID, video.Title); err != nil {
				log.Printf("Failed to mark video as downloaded: %v", err)
			}
			downloadCount++
		}
	}

	log.Printf("Channel %s: downloaded %d new videos, skipped %d already downloaded, skipped %d by filters/unavailable", channel.Name, downloadCount, skippedAlreadyDownloadedCount, skippedByDownloaderCount)
	if failedDownloadCount > 0 {
		return fmt.Errorf("failed to download %d video(s) for channel %s; first error: %w", failedDownloadCount, channel.Name, firstDownloadErr)
	}

	return nil
}

// processVideo checks and downloads a specific video if not already present
func processVideo(ctx context.Context, video Video, config *Config, storage *Storage, downloader *Downloader) error {
	log.Printf("Processing video: %s (retention: %d days)", video.Title, video.RetentionDays)

	// Check if we should proceed before starting work
	select {
	case <-ctx.Done():
		log.Printf("Shutdown signal received, skipping video %s", video.Title)
		return nil // Return nil to not count as error
	default:
	}

	if !storage.HasVideo(video.ID) {
		log.Printf("Video %s removed during processing; skipping", video.Title)
		return nil
	}

	// Always update last checked time when we attempt to process (but not on shutdown)
	defer func() {
		if ctx.Err() != nil {
			return
		}
		if err := storage.UpdateVideoLastChecked(video.ID, time.Now()); err != nil {
			log.Printf("Failed to update video last checked time: %v", err)
		}
	}()

	// Get video info to check if it needs downloading
	info, err := downloader.GetVideoInfo(video.URL)
	if err != nil {
		return err
	}

	// Skip if already downloaded
	if storage.IsVideoDownloaded(video.ID, info.ID) {
		return nil
	}

	// Retention is based on download date, so don't skip based on publish date

	// Download the video with the video's preferred quality and shorts settings
	channelName := info.Uploader
	if channelName == "" {
		channelName = "unknown"
	}

	if !storage.HasVideo(video.ID) {
		log.Printf("Video %s removed during processing; stopping download", video.Title)
		return nil
	}

	result, err := downloader.DownloadVideo(video.URL, info.ID, channelName, video.VideoQuality, video.VideoFormat, video.DownloadShorts)
	if err != nil {
		log.Printf("Failed to download video %s: %v", video.Title, err)
		// Don't mark as downloaded - will retry on next interval
		return err
	}

	if result != nil && result.Skipped {
		return nil
	}

	// Mark as downloaded
	if err := storage.MarkVideoAsDownloaded(video.ID, info.ID, info.Title); err != nil {
		log.Printf("Failed to mark video as downloaded: %v", err)
	}

	return nil
}
