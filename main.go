package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"ytdm/storage"
)

func main() {
	logBuffer := NewLogBuffer(100)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(io.MultiWriter(os.Stdout, logBuffer))
	log.Println("Starting YouTube Media Downloader")
	log.Printf("App commit: %s", getAppCommit())

	// Load configuration
	config, err := LoadConfig("data/config.json")
	if err != nil {
		log.Printf("Failed to load config: %v, using defaults", err)
		config = DefaultConfig()
		if err := config.Save("data/config.json"); err != nil {
			log.Printf("Failed to save default config: %v", err)
		}
	}
	logBuffer.SetMaxEntries(config.MaxLogEntries)
	log.Printf("Configuration loaded: %+v", config)

	// Initialize storage
	store, err := storage.NewStorage("data/data.db")
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()
	log.Println("Storage initialized")

	// Create downloader for migrations
	downloader := NewDownloader(config)

	// Run channel ID migration for any older entries
	migratedCount, migrationErrors := migrateChannelIDs(store, downloader)
	if migratedCount > 0 {
		log.Printf("Completed channel ID migration: %d channel(s) updated", migratedCount)
		if len(migrationErrors) > 0 {
			log.Printf("Migration completed with %d error(s)", len(migrationErrors))
			for _, errMsg := range migrationErrors {
				log.Printf("  - %s", errMsg)
			}
		}
	}

	unknownMigratedVideos, unknownMovedFiles, unknownMigrationErrors := downloader.MigrateUnknownVideos()
	if unknownMigratedVideos > 0 {
		log.Printf("Migrated %d video(s) from unknown folder (%d file(s) moved)", unknownMigratedVideos, unknownMovedFiles)
	}
	if len(unknownMigrationErrors) > 0 {
		log.Printf("Unknown-folder migration completed with %d warning(s)", len(unknownMigrationErrors))
		for _, errMsg := range unknownMigrationErrors {
			log.Printf("  - %s", errMsg)
		}
	}

	startupPrune := RunStartupChannelPruneScan(config, store)
	if startupPrune.VideosPruned > 0 || startupPrune.FilesRemoved > 0 || startupPrune.FilesMoved > 0 {
		log.Printf("Startup prune scan complete: removed %d tracked video(s), %d file(s), moved %d no-prune file(s)", startupPrune.VideosPruned, startupPrune.FilesRemoved, startupPrune.FilesMoved)
	} else {
		log.Println("Startup prune scan complete: no stale tracked channel files found")
	}
	if len(startupPrune.Warnings) > 0 {
		log.Printf("Startup prune scan had %d warning(s)", len(startupPrune.Warnings))
		for _, warning := range startupPrune.Warnings {
			log.Printf("  - %s", warning)
		}
	}

	if migratedToPruned := MigrateExpiredDownloadsToPruned(config, store); migratedToPruned > 0 {
		log.Printf("Migrated %d expired downloaded video(s) to pruned list (preventing re-download)", migratedToPruned)
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wait group for goroutines
	var wg sync.WaitGroup

	// Start the updater
	wg.Add(1)
	go func() {
		defer wg.Done()
		RunUpdater(ctx, config)
	}()
	log.Println("yt-dlp updater started")

	// Start the scheduler
	wg.Add(1)
	go func() {
		defer wg.Done()
		RunScheduler(ctx, config, store)
	}()
	log.Println("Scheduler started")

	// Start the API server
	wg.Add(1)
	go func() {
		defer wg.Done()
		StartAPIServer(ctx, config, store, logBuffer)
	}()
	log.Printf("API server started on port %d", config.APIPort)

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Println("Shutdown signal received, stopping gracefully...")
	log.Println("Note: In-progress video downloads will complete before shutdown")

	// Cancel context to stop goroutines
	cancel()

	// Wait for goroutines to finish with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("All goroutines stopped")
	case <-time.After(5 * time.Minute):
		log.Println("Shutdown timeout (5 minutes), forcing exit")
	}

	log.Println("YouTube Media Downloader stopped")
}

// migrateChannelIDs resolves and updates channel IDs for any channels that don't have
// proper UC... canonical IDs. This orchestration lives in package main (rather than as a
// storage.Storage method) because it needs *Downloader, which the storage package cannot
// import back from.
func migrateChannelIDs(store *storage.Storage, downloader *Downloader) (migratedCount int, errors []string) {
	var toMigrate []storage.Channel
	for _, ch := range store.GetChannels() {
		if !strings.HasPrefix(ch.ID, "UC") {
			toMigrate = append(toMigrate, ch)
		}
	}
	if len(toMigrate) == 0 {
		return 0, nil
	}

	log.Printf("Found %d channel(s) needing ID migration", len(toMigrate))

	for _, channel := range toMigrate {
		newID, err := downloader.ResolveChannelID(channel.URL)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to resolve channel ID for %s (%s): %v", channel.Name, channel.URL, err)
			log.Printf("Migration error: %s", errMsg)
			errors = append(errors, errMsg)
			continue
		}

		if err := store.UpdateChannelID(channel.ID, newID); err != nil {
			errMsg := fmt.Sprintf("Failed to update channel ID for %s: %v", channel.Name, err)
			log.Printf("Migration error: %s", errMsg)
			errors = append(errors, errMsg)
			continue
		}
		migratedCount++
	}

	return migratedCount, errors
}
