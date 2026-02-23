package main

import (
	"context"
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

	// Create a semaphore to limit concurrent downloads
	semaphore := make(chan struct{}, config.MaxConcurrent)
	var wg sync.WaitGroup

	// Monitor for shutdown signal
	go func() {
		<-ctx.Done()
		shutdownMu.Lock()
		shuttingDown = true
		shutdownMu.Unlock()
		log.Println("Shutdown signal received, finishing in-progress downloads...")
	}()

	// Check channels
	channels := storage.GetChannels()
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

	// Check individual videos
	videos := storage.GetVideos()
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

	// Wait for all downloads to complete
	log.Println("Waiting for in-progress downloads to complete...")
	wg.Wait()
	log.Println("All downloads completed")

	// Only clean old videos if not shutting down
	shutdownMu.RLock()
	if !shuttingDown {
		shutdownMu.RUnlock()
		// Clean old videos per channel
		for _, channel := range channels {
			if err := downloader.CleanOldVideosForChannel(channel.Name, channel.RetentionDays); err != nil {
				log.Printf("Error cleaning old videos for channel %s: %v", channel.Name, err)
			}
		}
	} else {
		shutdownMu.RUnlock()
		log.Println("Skipping cleanup due to shutdown")
	}

	log.Println("Scheduled check completed")
}

// processChannel checks a channel for new videos and downloads them
func processChannel(ctx context.Context, channel Channel, config *Config, storage *Storage, downloader *Downloader) error {
	log.Printf("Processing channel: %s (retention: %d days)", channel.Name, channel.RetentionDays)

	// Determine the time window to check for videos
	// Retention is based on download date, so don't filter by publish date unless a cutoff is set.
	var since time.Time
	if !channel.CutoffDate.IsZero() {
		log.Printf("Applying cutoff date for channel %s: %s (inclusive)", channel.Name, channel.CutoffDate.Format("2006-01-02"))
		// Subtract one day to include videos published on the cutoff date
		since = channel.CutoffDate.AddDate(0, 0, -1)
	}

	// Always check for videos in the retention/cutoff window
	videos, err := downloader.GetChannelVideos(channel.URL, since)
	if err != nil {
		return err
	}

	downloadCount := 0
	skippedCount := 0

	// Download each video that hasn't been downloaded yet
	for _, video := range videos {
		// Check if we should start a new download
		select {
		case <-ctx.Done():
			log.Printf("Shutdown signal received, skipping remaining videos for channel %s", channel.Name)
			return nil // Return nil to not count as error
		default:
		}

		// Skip if already downloaded
		if storage.IsVideoDownloaded(channel.ID, video.ID) {
			skippedCount++
			continue
		}

		// Download the video
		if err := downloader.DownloadVideo(video.ID, channel.Name); err != nil {
			log.Printf("Failed to download video %s: %v", video.Title, err)
			// Continue with other videos even if one fails
		} else {
			// Mark as downloaded
			if err := storage.MarkVideoAsDownloaded(channel.ID, video.ID); err != nil {
				log.Printf("Failed to mark video as downloaded: %v", err)
			}
			downloadCount++
		}
	}

	log.Printf("Channel %s: downloaded %d new videos, skipped %d already downloaded", channel.Name, downloadCount, skippedCount)

	// Update last checked time
	if err := storage.UpdateChannelLastChecked(channel.ID, time.Now()); err != nil {
		log.Printf("Failed to update channel last checked time: %v", err)
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

	// Get video info to check if it needs downloading
	info, err := downloader.GetVideoInfo(video.URL)
	if err != nil {
		return err
	}

	// Skip if already downloaded
	if storage.IsVideoDownloaded(video.ID, info.ID) {
		log.Printf("Video %s already downloaded, skipping", video.Title)
		return nil
	}

	// Retention is based on download date, so don't skip based on publish date

	// Download the video
	channelName := info.Uploader
	if channelName == "" {
		channelName = "unknown"
	}

	if err := downloader.DownloadVideo(video.URL, channelName); err != nil {
		log.Printf("Failed to download video %s: %v", video.Title, err)
		// Don't mark as downloaded - will retry on next interval
		return err
	}

	// Mark as downloaded
	if err := storage.MarkVideoAsDownloaded(video.ID, info.ID); err != nil {
		log.Printf("Failed to mark video as downloaded: %v", err)
	}

	// Update last checked time
	if err := storage.UpdateVideoLastChecked(video.ID, time.Now()); err != nil {
		log.Printf("Failed to update video last checked time: %v", err)
	}

	return nil
}
