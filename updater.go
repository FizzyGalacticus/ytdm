package main

import (
	"bytes"
	"context"
	"log"
	"os/exec"
	"time"
)

// RunUpdater periodically updates yt-dlp to the latest version
func RunUpdater(ctx context.Context, config *Config) {
	initialInterval := config.GetUpdateInterval()
	if initialInterval <= 0 {
		log.Println("yt-dlp auto-update is disabled")
		return
	}

	var ticker *time.Ticker
	var lastInterval time.Duration

	ticker = time.NewTicker(initialInterval)
	lastInterval = initialInterval
	defer ticker.Stop()

	log.Printf("yt-dlp updater started, update interval: %v", initialInterval)

	// Run initial update after a short delay
	time.Sleep(10 * time.Second)
	updateYtDlp(config.YtDlpPath)

	for {
		select {
		case <-ctx.Done():
			log.Println("yt-dlp updater stopping...")
			return
		case <-ticker.C:
			// Check if interval has changed
			currentInterval := config.GetUpdateInterval()
			if currentInterval <= 0 {
				log.Println("yt-dlp auto-update disabled via config change")
				ticker.Stop()
				return
			}

			if currentInterval != lastInterval {
				log.Printf("Update interval changed from %v to %v, restarting ticker", lastInterval, currentInterval)
				ticker.Stop()
				ticker = time.NewTicker(currentInterval)
				lastInterval = currentInterval
			}

			updateYtDlp(config.YtDlpPath)
		}
	}
}

// updateYtDlp attempts to update yt-dlp using its built-in update mechanism
func updateYtDlp(ytDlpPath string) {
	log.Println("Checking for yt-dlp updates...")

	cmd := exec.Command(ytDlpPath, "-U")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Printf("Failed to update yt-dlp: %v, stderr: %s", err, stderr.String())
		return
	}

	output := stdout.String()
	if output != "" {
		log.Printf("yt-dlp update result: %s", output)
	} else {
		log.Println("yt-dlp is up to date")
	}
}
