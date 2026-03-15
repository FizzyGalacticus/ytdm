package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
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
	log.Printf("Configuration loaded: %+v", config)

	// Initialize storage
	storage, err := NewStorage("data/data.json")
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	log.Println("Storage initialized")

	// Create downloader for migrations
	downloader := NewDownloader(config)

	// Run channel ID migration for any older entries
	migratedCount, migrationErrors := storage.MigrateChannelIDs(downloader)
	if migratedCount > 0 {
		log.Printf("Completed channel ID migration: %d channel(s) updated", migratedCount)
		if len(migrationErrors) > 0 {
			log.Printf("Migration completed with %d error(s)", len(migrationErrors))
			for _, errMsg := range migrationErrors {
				log.Printf("  - %s", errMsg)
			}
		}
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
		RunScheduler(ctx, config, storage)
	}()
	log.Println("Scheduler started")

	// Start the API server
	wg.Add(1)
	go func() {
		defer wg.Done()
		StartAPIServer(ctx, config, storage, logBuffer)
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
