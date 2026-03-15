package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

// APIResponse is a generic response structure
type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// APIServer holds the server state
type APIServer struct {
	config  *Config
	storage *Storage
	server  *http.Server
	logs    *LogBuffer
}

// StartAPIServer starts the HTTP API server
func StartAPIServer(ctx context.Context, config *Config, storage *Storage, logs *LogBuffer) {
	api := &APIServer{
		config:  config,
		storage: storage,
		logs:    logs,
	}

	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/channels", api.handleChannels)
	mux.HandleFunc("/api/channels/", api.handleChannelByID)
	mux.HandleFunc("/api/videos", api.handleVideos)
	mux.HandleFunc("/api/videos/", api.handleVideoByID)
	mux.HandleFunc("/api/config", api.handleConfig)
	mux.HandleFunc("/api/cookies", api.handleCookies)
	mux.HandleFunc("/api/cookies/clear", api.handleClearCookies)
	mux.HandleFunc("/api/status", api.handleStatus)
	mux.HandleFunc("/api/logs", api.handleLogs)

	// Static file server from embedded files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to load embedded static files: %v", err)
	}
	fs := http.FileServer(http.FS(staticFS))
	mux.Handle("/", fs)

	api.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", config.APIPort),
		Handler: mux,
	}

	// Start server in goroutine
	go func() {
		log.Printf("API server listening on :%d", config.APIPort)
		if err := api.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("API server error: %v", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := api.server.Shutdown(shutdownCtx); err != nil {
		log.Printf("API server shutdown error: %v", err)
	}
	log.Println("API server stopped")
}

// handleLogs returns recent in-memory log lines
func (api *APIServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	entries := []string{}
	if api.logs != nil {
		entries = api.logs.GetEntries()
	}

	api.sendSuccess(w, map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
	})
}

// handleChannels handles GET and POST for channels
func (api *APIServer) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.getChannels(w, r)
	case http.MethodPost:
		api.addChannel(w, r)
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// getChannels returns all channels
func (api *APIServer) getChannels(w http.ResponseWriter, r *http.Request) {
	channels := api.storage.GetChannels()
	api.sendSuccess(w, channels)
}

// addChannel adds a new channel
func (api *APIServer) addChannel(w http.ResponseWriter, r *http.Request) {
	var channel Channel
	if err := json.NewDecoder(r.Body).Decode(&channel); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Resolve canonical channel ID (UC...) so RSS lookups work for @handle URLs.
	if channel.URL != "" {
		downloader := NewDownloader(api.config)
		resolvedID, err := downloader.ResolveChannelID(channel.URL)
		if err == nil && resolvedID != "" {
			channel.ID = resolvedID
		} else if channel.ID == "" {
			channel.ID = extractIDFromURL(channel.URL)
			if err != nil {
				log.Printf("Warning: failed to resolve canonical channel ID for %s, using fallback id %s: %v", channel.URL, channel.ID, err)
			}
		}
	}

	if channel.ID == "" {
		api.sendError(w, http.StatusBadRequest, "Could not determine channel ID from URL")
		return
	}

	// Use default retention if not specified
	if channel.RetentionDays == 0 {
		channel.RetentionDays = api.config.RetentionDays
	}

	// Use default format if not specified
	if channel.VideoFormat == "" {
		channel.VideoFormat = api.config.DefaultVideoFormat
	}

	if err := api.storage.AddChannel(channel); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to add channel: %v", err))
		return
	}

	log.Printf("Channel added via API: %s", channel.Name)
	api.sendSuccess(w, channel)
}

// handleChannelByID handles DELETE and PUT for a specific channel
func (api *APIServer) handleChannelByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		api.sendError(w, http.StatusBadRequest, "Invalid channel ID")
		return
	}
	id := parts[3]

	switch r.Method {
	case http.MethodDelete:
		var target *Channel
		for _, ch := range api.storage.GetChannels() {
			if ch.ID == id {
				c := ch
				target = &c
				break
			}
		}

		if target != nil {
			downloader := NewDownloader(api.config)
			if err := downloader.RemoveChannelResources(*target); err != nil {
				api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to remove channel resources: %v", err))
				return
			}
		}

		if err := api.storage.RemoveChannel(id); err != nil {
			api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to remove channel: %v", err))
			return
		}
		log.Printf("Channel removed via API: %s", id)
		api.sendSuccess(w, map[string]string{"id": id})
	case http.MethodPut:
		api.updateChannel(w, r, id)
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// updateChannel updates channel settings (retention days, cutoff date, video quality, video format, shorts preference)
func (api *APIServer) updateChannel(w http.ResponseWriter, r *http.Request, id string) {
	var updateData struct {
		RetentionDays  int       `json:"retention_days"`
		CutoffDate     time.Time `json:"cutoff_date"`
		VideoQuality   string    `json:"video_quality"`
		VideoFormat    string    `json:"video_format"`
		DownloadShorts bool      `json:"download_shorts"`
	}

	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := api.storage.UpdateChannel(id, updateData.RetentionDays, updateData.CutoffDate, updateData.VideoQuality, updateData.VideoFormat, updateData.DownloadShorts); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to update channel: %v", err))
		return
	}

	log.Printf("Channel updated via API: %s", id)
	api.sendSuccess(w, map[string]string{"id": id})
}

// handleVideos handles GET and POST for videos
func (api *APIServer) handleVideos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.getVideos(w, r)
	case http.MethodPost:
		api.addVideo(w, r)
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// getVideos returns all videos
func (api *APIServer) getVideos(w http.ResponseWriter, r *http.Request) {
	videos := api.storage.GetVideos()
	api.sendSuccess(w, videos)
}

// addVideo adds a new video
func (api *APIServer) addVideo(w http.ResponseWriter, r *http.Request) {
	var video Video
	if err := json.NewDecoder(r.Body).Decode(&video); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Generate ID from URL if not provided
	if video.ID == "" {
		video.ID = extractIDFromURL(video.URL)
	}

	// Fetch video title from yt-dlp if not provided
	if video.Title == "" {
		downloader := NewDownloader(api.config)
		info, err := downloader.GetVideoInfo(video.URL)
		if err != nil {
			api.sendError(w, http.StatusBadRequest, fmt.Sprintf("Failed to fetch video info: %v", err))
			return
		}
		video.Title = info.Title
		if video.ID == "" || video.ID == extractIDFromURL(video.URL) {
			video.ID = info.ID // Use the more reliable ID from yt-dlp
		}
	}

	// Use default retention if not specified
	if video.RetentionDays == 0 {
		video.RetentionDays = api.config.RetentionDays
	}

	// Use default format if not specified
	if video.VideoFormat == "" {
		video.VideoFormat = api.config.DefaultVideoFormat
	}

	// DownloadShorts defaults to true if not explicitly disabled
	if !video.DownloadShorts && video.VideoQuality == "" {
		video.DownloadShorts = true
	}

	if err := api.storage.AddVideo(video); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to add video: %v", err))
		return
	}

	log.Printf("Video added via API: %s", video.Title)
	api.sendSuccess(w, video)
}

// handleVideoByID handles DELETE for a specific video
func (api *APIServer) handleVideoByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		api.sendError(w, http.StatusBadRequest, "Invalid video ID")
		return
	}
	id := parts[3]

	switch r.Method {
	case http.MethodDelete:
		var target *Video
		for _, v := range api.storage.GetVideos() {
			if v.ID == id {
				vv := v
				target = &vv
				break
			}
		}

		if target != nil {
			downloader := NewDownloader(api.config)
			if err := downloader.RemoveVideoResources(*target); err != nil {
				api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to remove video resources: %v", err))
				return
			}
		}

		if err := api.storage.RemoveVideo(id); err != nil {
			api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to remove video: %v", err))
			return
		}
		log.Printf("Video removed via API: %s", id)
		api.sendSuccess(w, map[string]string{"id": id})
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleConfig handles GET and PUT for configuration
func (api *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.getConfig(w, r)
	case http.MethodPut:
		api.updateConfig(w, r)
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// getConfig returns the current configuration
func (api *APIServer) getConfig(w http.ResponseWriter, r *http.Request) {
	configData := map[string]interface{}{
		"check_interval_seconds":   api.config.CheckInterval,
		"retention_days":           api.config.RetentionDays,
		"download_dir":             api.config.DownloadDir,
		"file_name_pattern":        api.config.FileNamePattern,
		"api_port":                 api.config.APIPort,
		"max_concurrent_downloads": api.config.MaxConcurrent,
		"yt_dlp": map[string]interface{}{
			"path":                             api.config.YtDlp.Path,
			"update_interval_seconds":          api.config.YtDlp.UpdateInterval,
			"cookies_browser":                  api.config.YtDlp.CookiesBrowser,
			"cookies_file":                     api.config.YtDlp.CookiesFile,
			"extractor_sleep_interval_seconds": api.config.YtDlp.ExtractorSleepInterval,
			"download_throughput_limit":        api.config.YtDlp.DownloadThroughputLimit,
			"restrict_filenames":               api.config.YtDlp.RestrictFilenames,
			"cache_dir":                        api.config.YtDlp.CacheDir,
		},
	}
	api.sendSuccess(w, configData)
}

// updateConfig updates the configuration
func (api *APIServer) updateConfig(w http.ResponseWriter, r *http.Request) {
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Update config fields
	if val, ok := updates["check_interval_seconds"].(string); ok {
		api.config.CheckInterval = val
	}
	if val, ok := updates["retention_days"].(float64); ok {
		api.config.RetentionDays = int(val)
	}
	if val, ok := updates["download_dir"].(string); ok {
		api.config.DownloadDir = val
	}
	if val, ok := updates["file_name_pattern"].(string); ok {
		api.config.FileNamePattern = val
	}
	if val, ok := updates["max_concurrent_downloads"].(float64); ok {
		api.config.MaxConcurrent = int(val)
	}
	if ytDlpRaw, ok := updates["yt_dlp"].(map[string]interface{}); ok {
		if val, ok := ytDlpRaw["path"].(string); ok {
			api.config.YtDlp.Path = val
		}
		if val, ok := ytDlpRaw["update_interval_seconds"].(string); ok {
			api.config.YtDlp.UpdateInterval = val
		}
		if val, ok := ytDlpRaw["cookies_browser"].(string); ok {
			api.config.YtDlp.CookiesBrowser = val
		}
		if val, ok := ytDlpRaw["cookies_file"].(string); ok {
			api.config.YtDlp.CookiesFile = val
		}
		if val, ok := ytDlpRaw["extractor_sleep_interval_seconds"].(string); ok {
			api.config.YtDlp.ExtractorSleepInterval = val
		}
		if val, ok := ytDlpRaw["download_throughput_limit"].(string); ok {
			api.config.YtDlp.DownloadThroughputLimit = val
		}
		if val, ok := ytDlpRaw["restrict_filenames"].(bool); ok {
			api.config.YtDlp.RestrictFilenames = val
		}
		if val, ok := ytDlpRaw["cache_dir"].(string); ok {
			api.config.YtDlp.CacheDir = val
		}
	}

	// Save config
	if err := api.config.Save("data/config.json"); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save config: %v", err))
		return
	}

	// Reload config from disk to ensure consistency
	if err := api.config.ReloadFromDisk("data/config.json"); err != nil {
		log.Printf("Warning: Failed to reload config from disk: %v", err)
	}

	log.Println("Configuration updated via API and reloaded")
	api.sendSuccess(w, map[string]string{"message": "Configuration updated and reloaded"})
}

// handleCookies handles POST requests to save Netscape format cookies
func (api *APIServer) handleCookies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	type cookieRequest struct {
		CookieText string `json:"cookie_text"`
	}

	var req cookieRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.sendError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request: %v", err))
		return
	}

	// Save cookies to data/cookies.txt
	cookiePath := "data/cookies.txt"
	if err := os.WriteFile(cookiePath, []byte(req.CookieText), 0644); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save cookies: %v", err))
		return
	}

	// Update config to use the cookies file
	api.config.Lock()
	api.config.YtDlp.CookiesBrowser = ""
	api.config.YtDlp.CookiesFile = "data/cookies.txt"
	api.config.Unlock()

	// Save config to disk
	if err := api.config.Save("data/config.json"); err != nil {
		log.Printf("Warning: Failed to save config: %v", err)
	}

	api.sendSuccess(w, map[string]string{"message": "Cookies saved successfully"})
}

// handleClearCookies handles POST requests to clear all cookies
func (api *APIServer) handleClearCookies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Clear cookies file
	cookiePath := "data/cookies.txt"
	if err := os.WriteFile(cookiePath, []byte("# Netscape HTTP Cookie File\n# Cookies cleared\n"), 0644); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to clear cookies: %v", err))
		return
	}

	// Update config to disable cookies
	api.config.Lock()
	api.config.YtDlp.CookiesBrowser = ""
	api.config.YtDlp.CookiesFile = ""
	api.config.Unlock()

	// Save config to disk
	if err := api.config.Save("data/config.json"); err != nil {
		log.Printf("Warning: Failed to save config: %v", err)
	}

	api.sendSuccess(w, map[string]string{"message": "Cookies cleared successfully"})
}

// handleStatus returns the current status
func (api *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	status := map[string]interface{}{
		"channels_count":   len(api.storage.GetChannels()),
		"videos_count":     len(api.storage.GetVideos()),
		"uptime":           time.Now().Format(time.RFC3339),
		"yt_dlp_version":   getYtDlpVersion(api.config.YtDlp.Path),
		"app_commit":       getAppCommit(),
		"app_commit_short": getShortAppCommit(),
	}
	api.sendSuccess(w, status)
}

func getYtDlpVersion(ytDlpPath string) string {
	if ytDlpPath == "" {
		return "unknown"
	}

	cmd := exec.Command(ytDlpPath, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Printf("Failed to get yt-dlp version: %v, stderr: %s", err, stderr.String())
		return "unknown"
	}

	version := strings.TrimSpace(stdout.String())
	if version == "" {
		return "unknown"
	}

	return version
}

// sendSuccess sends a successful JSON response
func (api *APIServer) sendSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(APIResponse{
		Success: true,
		Data:    data,
	})
}

// sendError sends an error JSON response
func (api *APIServer) sendError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(APIResponse{
		Success: false,
		Message: message,
	})
}

// extractIDFromURL extracts the video/channel ID from a YouTube URL
func extractIDFromURL(url string) string {
	// Simple extraction - works for most YouTube URLs
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		lastPart := parts[len(parts)-1]
		// Remove query parameters
		if idx := strings.Index(lastPart, "?"); idx != -1 {
			return lastPart[:idx]
		}
		return lastPart
	}
	return url
}
