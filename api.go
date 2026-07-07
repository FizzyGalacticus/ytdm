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
	"path/filepath"
	"sort"
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
	mux.HandleFunc("/api/videos/convert-to-channel", api.handleConvertToChannel)
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
	structuredEntries := []LogEntry{}
	scopes := []LogScope{}
	scopeType := strings.TrimSpace(r.URL.Query().Get("scope_type"))
	scopeID := strings.TrimSpace(r.URL.Query().Get("scope_id"))
	if api.logs != nil {
		structuredEntries = api.logs.GetStructuredEntries(scopeType, scopeID)
		entries = make([]string, 0, len(structuredEntries))
		for _, entry := range structuredEntries {
			entries = append(entries, entry.Line)
		}

		// Start with scopes that have log entries, then fill in any remaining channels.
		logScopes := api.logs.GetScopes()
		seen := make(map[string]bool, len(logScopes))
		for _, s := range logScopes {
			seen[s.Type+":"+s.ID] = true
		}
		scopes = logScopes
		for _, ch := range api.storage.GetChannels() {
			if !seen["channel:"+ch.ID] {
				scopes = append(scopes, LogScope{Type: "channel", ID: ch.ID, Name: ch.Name})
			}
		}
		sort.Slice(scopes, func(i, j int) bool {
			ni, nj := scopes[i].Name, scopes[j].Name
			if ni == "" {
				ni = scopes[i].ID
			}
			if nj == "" {
				nj = scopes[j].ID
			}
			return strings.ToLower(ni) < strings.ToLower(nj)
		})
	}

	api.sendSuccess(w, map[string]interface{}{
		"entries":            entries,
		"structured_entries": structuredEntries,
		"scopes":             scopes,
		"count":              len(entries),
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
		if extractedID, err := extractChannelID(channel.URL); err == nil && strings.HasPrefix(extractedID, "UC") {
			channel.ID = extractedID
		} else {
			downloader := NewDownloader(api.config)
			resolvedID, resolveErr := downloader.ResolveChannelID(channel.URL)
			if resolveErr == nil && resolvedID != "" {
				channel.ID = resolvedID
			} else if channel.ID == "" {
				channel.ID = extractIDFromURL(channel.URL)
				if resolveErr != nil {
					log.Printf("Warning: failed to resolve canonical channel ID for %s, using fallback id %s: %v", channel.URL, channel.ID, resolveErr)
				}
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

	logScopef("channel", channel.ID, channel.Name, "Channel added via API: %s", channel.Name)
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

	// Handle channel downloaded-video subresource: /api/channels/{id}/videos/{videoId}
	if len(parts) >= 6 && parts[4] == "videos" {
		videoID := parts[5]
		switch r.Method {
		case http.MethodPut:
			api.updateChannelDownloadedVideo(w, r, id, videoID)
		default:
			api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	// Handle manual feed-video download: POST /api/channels/{id}/feed-videos/{videoId}/download
	if len(parts) >= 7 && parts[4] == "feed-videos" && parts[6] == "download" {
		videoID := parts[5]
		if r.Method == http.MethodPost {
			api.handleManualFeedVideoDownload(w, r, id, videoID)
		} else {
			api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	// Handle feed-video dismissal: POST /api/channels/{id}/feed-videos/{videoId}/dismiss
	if len(parts) >= 7 && parts[4] == "feed-videos" && parts[6] == "dismiss" {
		videoID := parts[5]
		if r.Method == http.MethodPost {
			api.handleDismissFeedVideo(w, r, id, videoID)
		} else {
			api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

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
		logScopef("channel", id, id, "Channel removed via API: %s", id)
		api.sendSuccess(w, map[string]string{"id": id})
	case http.MethodPut:
		api.updateChannel(w, r, id)
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// updateChannelDownloadedVideo updates per-downloaded-video settings for a channel entry.
func (api *APIServer) updateChannelDownloadedVideo(w http.ResponseWriter, r *http.Request, channelID, videoID string) {
	var updateData struct {
		DisablePruning bool `json:"disable_pruning"`
	}

	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := api.storage.UpdateChannelDownloadedVideoPruning(channelID, videoID, updateData.DisablePruning); err != nil {
		api.sendError(w, http.StatusNotFound, fmt.Sprintf("Failed to update channel downloaded video: %v", err))
		return
	}

	logScopef("channel", channelID, channelID, "Channel downloaded video updated via API: channel=%s video=%s disable_pruning=%v", channelID, videoID, updateData.DisablePruning)
	api.sendSuccess(w, map[string]interface{}{
		"channel_id":      channelID,
		"video_id":        videoID,
		"disable_pruning": updateData.DisablePruning,
	})
}

// updateChannel updates channel settings (retention days, pruning, cutoff date, video quality, video format, shorts preference, auto-download)
func (api *APIServer) updateChannel(w http.ResponseWriter, r *http.Request, id string) {
	var updateData struct {
		RetentionDays    int       `json:"retention_days"`
		DisablePruning   bool      `json:"disable_pruning"`
		CutoffDate       time.Time `json:"cutoff_date"`
		VideoQuality     string    `json:"video_quality"`
		VideoFormat      string    `json:"video_format"`
		DownloadShorts   bool      `json:"download_shorts"`
		SkipAutoDownload bool      `json:"skip_auto_download"`
	}

	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := api.storage.UpdateChannel(id, updateData.RetentionDays, updateData.DisablePruning, updateData.CutoffDate, updateData.VideoQuality, updateData.VideoFormat, updateData.DownloadShorts, updateData.SkipAutoDownload); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to update channel: %v", err))
		return
	}

	logScopef("channel", id, id, "Channel updated via API: %s", id)
	api.sendSuccess(w, map[string]string{"id": id})
}

// handleManualFeedVideoDownload triggers a manual download for a specific feed video.
// The download runs asynchronously; a 200 response means the job was queued.
func (api *APIServer) handleManualFeedVideoDownload(w http.ResponseWriter, r *http.Request, channelID, videoID string) {
	var targetChannel *Channel
	for _, ch := range api.storage.GetChannels() {
		if ch.ID == channelID {
			c := ch
			targetChannel = &c
			break
		}
	}
	if targetChannel == nil {
		api.sendError(w, http.StatusNotFound, "Channel not found")
		return
	}

	var feedVideo *FeedVideo
	for _, fv := range targetChannel.FeedVideos {
		if fv.ID == videoID {
			v := fv
			feedVideo = &v
			break
		}
	}
	if feedVideo == nil {
		api.sendError(w, http.StatusNotFound, "Feed video not found or already downloaded")
		return
	}

	ch := *targetChannel
	fv := *feedVideo
	go func() {
		dl := NewDownloader(api.config)
		result, err := dl.DownloadVideo(fv.URL, fv.ID, ch.Name, ch.VideoQuality, ch.VideoFormat, true, false)
		if err != nil {
			logScopef("channel", ch.ID, ch.Name, "Manual download failed for video %s: %v", fv.ID, err)
			if setErr := api.storage.SetChannelError(ch.ID, fmt.Sprintf("Manual download of %q failed: %v", fv.Title, err)); setErr != nil {
				log.Printf("Failed to set channel error after manual download failure: %v", setErr)
			}
			return
		}
		if result != nil && result.Skipped {
			logScopef("channel", ch.ID, ch.Name, "Manual download skipped for video %s: %s", fv.ID, result.SkipReason)
			return
		}
		if err := api.storage.MarkVideoAsDownloaded(ch.ID, fv.ID, fv.Title, fv.PublishedAt); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to mark manually downloaded video %s: %v", fv.ID, err)
		}
		if err := api.storage.RemoveFeedVideo(ch.ID, fv.ID); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to remove feed video %s after manual download: %v", fv.ID, err)
		}
		if result != nil && result.ChannelIcon != "" {
			if err := api.storage.SetChannelThumbnailIfEmpty(ch.ID, result.ChannelIcon); err != nil {
				logScopef("channel", ch.ID, ch.Name, "Failed to update thumbnail after manual download: %v", err)
			}
		}
		logScopef("channel", ch.ID, ch.Name, "Manual download completed for video %s (%s)", fv.ID, fv.Title)
	}()

	logScopef("channel", ch.ID, ch.Name, "Manual download queued for video %s (%s)", fv.ID, fv.Title)
	api.sendSuccess(w, map[string]string{"video_id": fv.ID, "message": "Download started"})
}

// handleDismissFeedVideo permanently dismisses a pending feed video: it is removed
// from the channel's pending list and recorded as pruned so it is never re-downloaded
// or re-surfaced by future RSS scans.
func (api *APIServer) handleDismissFeedVideo(w http.ResponseWriter, r *http.Request, channelID, videoID string) {
	var feedVideo *FeedVideo
	for _, ch := range api.storage.GetChannels() {
		if ch.ID != channelID {
			continue
		}
		for _, fv := range ch.FeedVideos {
			if fv.ID == videoID {
				v := fv
				feedVideo = &v
			}
		}
		break
	}
	if feedVideo == nil {
		api.sendError(w, http.StatusNotFound, "Feed video not found")
		return
	}

	if err := api.storage.AddPrunedVideo(channelID, videoID, feedVideo.PublishedAt); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to dismiss video: %v", err))
		return
	}
	if err := api.storage.RemoveFeedVideo(channelID, videoID); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to dismiss video: %v", err))
		return
	}

	logScopef("channel", channelID, channelID, "Feed video dismissed via API: %s", videoID)
	api.sendSuccess(w, map[string]string{"video_id": videoID, "message": "Video dismissed"})
}

// handleConvertToChannel converts a group of individual video entries into a channel subscription.
// The individual video files remain on disk; only the storage entries are migrated.
func (api *APIServer) handleConvertToChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		UploaderName  string   `json:"uploader_name"`
		UploaderID    string   `json:"uploader_id"`
		VideoIDs      []string `json:"video_ids"`
		VideoQuality  string   `json:"video_quality"`
		VideoFormat   string   `json:"video_format"`
		RetentionDays int      `json:"retention_days"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(req.VideoIDs) == 0 {
		api.sendError(w, http.StatusBadRequest, "No video IDs provided")
		return
	}
	if req.UploaderID == "" {
		api.sendError(w, http.StatusBadRequest, "Uploader ID is required")
		return
	}

	// Collect DownloadedVideos entries from the individual video records so the
	// new channel does not re-download content that is already on disk.
	allVideos := api.storage.GetVideos()
	videoMap := make(map[string]Video, len(allVideos))
	for _, v := range allVideos {
		videoMap[v.ID] = v
	}

	var downloadedVideos []DownloadedVideo
	var earliestPublishDate time.Time
	// Only inherit the no-prune flag if every video being converted has it set;
	// a single prunable video means the channel should remain prunable.
	disablePruning := true
	matchedAny := false
	for _, videoID := range req.VideoIDs {
		if v, ok := videoMap[videoID]; ok {
			matchedAny = true
			// A video's own DisablePruning flag only ever gated its whole standalone
			// prune cycle; it was never stamped onto its DownloadedVideo records. Do
			// that here so per-video protection survives the move into a channel,
			// where pruning is decided per-record rather than per-video.
			for _, dv := range v.DownloadedVideos {
				if v.DisablePruning {
					dv.DisablePruning = true
				}
				downloadedVideos = append(downloadedVideos, dv)
				if !dv.PublishDate.IsZero() && (earliestPublishDate.IsZero() || dv.PublishDate.Before(earliestPublishDate)) {
					earliestPublishDate = dv.PublishDate
				}
			}
			if !v.DisablePruning {
				disablePruning = false
			}
		}
	}
	if !matchedAny {
		disablePruning = false
	}

	var channelURL string
	switch {
	case strings.HasPrefix(req.UploaderID, "UC"):
		channelURL = "https://www.youtube.com/channel/" + req.UploaderID
	case strings.HasPrefix(req.UploaderID, "@"):
		channelURL = "https://www.youtube.com/" + req.UploaderID
	default:
		channelURL = "https://www.youtube.com/channel/" + req.UploaderID
	}

	channelName := req.UploaderName
	if channelName == "" {
		channelName = req.UploaderID
	}

	dl := NewDownloader(api.config)

	var channelID string
	if extractedID, err := extractChannelID(channelURL); err == nil && strings.HasPrefix(extractedID, "UC") {
		channelID = extractedID
	} else {
		resolvedID, resolveErr := dl.ResolveChannelID(channelURL)
		if resolveErr == nil && resolvedID != "" {
			channelID = resolvedID
		} else {
			channelID = extractIDFromURL(channelURL)
			if resolveErr != nil {
				log.Printf("Warning: failed to resolve channel ID for %s, using fallback %s: %v", channelURL, channelID, resolveErr)
			}
		}
	}

	if channelID == "" {
		api.sendError(w, http.StatusBadRequest, "Could not determine channel ID from uploader ID")
		return
	}

	if api.storage.HasChannel(channelID) {
		// Channel already exists: merge pre-tracked downloads then remove individual entries.
		if len(downloadedVideos) > 0 {
			if err := api.storage.MergeChannelDownloadedVideos(channelID, downloadedVideos); err != nil {
				api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to merge channel videos: %v", err))
				return
			}
		}
		logScopef("channel", channelID, channelName, "Merged %d pre-tracked videos into existing channel during convert-to-channel", len(downloadedVideos))
		for _, videoID := range req.VideoIDs {
			if err := api.storage.RemoveVideo(videoID); err != nil {
				log.Printf("Warning: failed to remove video entry %s during convert-to-channel merge: %v", videoID, err)
			}
		}
		for _, ch := range api.storage.GetChannels() {
			if ch.ID == channelID {
				api.sendSuccess(w, ch)
				return
			}
		}
		api.sendSuccess(w, map[string]string{"id": channelID})
		return
	}

	retentionDays := req.RetentionDays
	if retentionDays == 0 {
		retentionDays = api.config.RetentionDays
	}
	videoFormat := req.VideoFormat
	if videoFormat == "" {
		videoFormat = api.config.DefaultVideoFormat
	}

	channelDir := filepath.Join(api.config.DownloadDir, sanitizeFilename(channelName))
	thumbnailURL := dl.GetChannelThumbnailFromInfoJSON(channelDir)

	cutoffDate := earliestPublishDate
	if cutoffDate.IsZero() {
		cutoffDate = time.Now()
	}

	channel := Channel{
		ID:               channelID,
		URL:              channelURL,
		Name:             channelName,
		RetentionDays:    retentionDays,
		DisablePruning:   disablePruning,
		VideoQuality:     req.VideoQuality,
		VideoFormat:      videoFormat,
		ThumbnailURL:     thumbnailURL,
		DownloadedVideos: downloadedVideos,
		CutoffDate:       cutoffDate,
	}

	if err := api.storage.AddChannel(channel); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to add channel: %v", err))
		return
	}

	logScopef("channel", channel.ID, channel.Name, "Channel created via convert-to-channel: %s (%d videos pre-tracked)", channel.Name, len(downloadedVideos))

	for _, videoID := range req.VideoIDs {
		if err := api.storage.RemoveVideo(videoID); err != nil {
			log.Printf("Warning: failed to remove video entry %s during convert-to-channel: %v", videoID, err)
		}
	}

	api.sendSuccess(w, channel)
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

	if video.AddedDate.IsZero() {
		video.AddedDate = time.Now().UTC()
	}

	// Generate ID from URL if not provided
	if video.ID == "" {
		video.ID = extractYouTubeVideoID(video.URL)
		if video.ID == "" {
			video.ID = extractIDFromURL(video.URL)
		}
	}

	// Fetch video title/uploader via oEmbed first, then fall back to yt-dlp.
	var fetchedInfo *VideoInfo
	if video.Title == "" {
		downloader := NewDownloader(api.config)

		if liteInfo, liteErr := downloader.GetVideoInfoLite(video.URL); liteErr == nil {
			fetchedInfo = liteInfo
			video.Title = strings.TrimSpace(liteInfo.Title)
			if video.ID == "" && strings.TrimSpace(liteInfo.ID) != "" {
				video.ID = strings.TrimSpace(liteInfo.ID)
			}
			if video.Uploader == "" {
				video.Uploader = strings.TrimSpace(liteInfo.Uploader)
			}
			if video.UploaderID == "" {
				video.UploaderID = strings.TrimSpace(liteInfo.UploaderID)
			}
		} else {
			info, err := downloader.GetVideoInfo(video.URL)
			if err != nil {
				api.sendError(w, http.StatusBadRequest, fmt.Sprintf("Failed to fetch video info: %v", err))
				return
			}
			fetchedInfo = info
			video.Title = strings.TrimSpace(info.Title)
			if video.ID == "" || video.ID == extractIDFromURL(video.URL) {
				video.ID = strings.TrimSpace(info.ID) // Use the more reliable ID from yt-dlp
			}
			if video.Uploader == "" {
				video.Uploader = strings.TrimSpace(info.Uploader)
			}
			if video.UploaderID == "" {
				video.UploaderID = strings.TrimSpace(info.UploaderID)
			}
		}

		if video.Title == "" {
			api.sendError(w, http.StatusBadRequest, "Failed to determine video title")
			return
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

	// Explicit single-video requests should not be blocked by shorts filtering.
	video.DownloadShorts = true

	// If the uploader's channel is already tracked, this video belongs under
	// that channel rather than as a standalone individual-video entry. UploaderID
	// alone is not reliable for this check: oEmbed and modern yt-dlp both commonly
	// report the @handle form rather than the canonical UC... ID that tracked
	// channels are keyed by, so resolve a canonical ID before matching.
	canonicalChannelID, resolvedInfo := api.resolveCanonicalChannelID(video.URL, fetchedInfo, video.UploaderID)
	if resolvedInfo != nil {
		fetchedInfo = resolvedInfo
	}
	if canonicalChannelID != "" && api.storage.HasChannel(canonicalChannelID) {
		api.addVideoUnderTrackedChannel(w, video, canonicalChannelID, fetchedInfo)
		return
	}

	if err := api.storage.AddVideo(video); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to add video: %v", err))
		return
	}

	logScopef("video", video.ID, video.Title, "Video added via API: %s", video.Title)
	api.sendSuccess(w, video)
}

// resolveCanonicalChannelID returns a canonical (UC...) channel ID for a video, reusing
// already-fetched metadata when possible. oEmbed's author_url and yt-dlp's own uploader_id
// commonly report the @handle form rather than the canonical ID that tracked channels are
// keyed by, so a dedicated yt-dlp lookup (whose result is returned for callers to reuse,
// e.g. for publish date) is used only when the info on hand doesn't already have one.
func (api *APIServer) resolveCanonicalChannelID(videoURL string, info *VideoInfo, uploaderID string) (string, *VideoInfo) {
	if info != nil && strings.HasPrefix(info.ChannelID, "UC") {
		return info.ChannelID, nil
	}
	if strings.HasPrefix(uploaderID, "UC") {
		return uploaderID, nil
	}

	downloader := NewDownloader(api.config)
	fullInfo, err := downloader.GetVideoInfo(videoURL)
	if err != nil {
		return "", nil
	}
	if strings.HasPrefix(fullInfo.ChannelID, "UC") {
		return fullInfo.ChannelID, fullInfo
	}
	if strings.HasPrefix(fullInfo.UploaderID, "UC") {
		return fullInfo.UploaderID, fullInfo
	}
	return "", fullInfo
}

// addVideoUnderTrackedChannel handles a manually-added video whose uploader channel is
// already tracked: it registers the video against that channel and downloads it using
// the channel's quality/format settings, rather than creating a separate individual entry.
func (api *APIServer) addVideoUnderTrackedChannel(w http.ResponseWriter, video Video, channelID string, fetchedInfo *VideoInfo) {
	var channelName, quality, format string
	for _, ch := range api.storage.GetChannels() {
		if ch.ID == channelID {
			channelName = ch.Name
			quality = ch.VideoQuality
			format = ch.VideoFormat
			break
		}
	}

	publishTime := time.Time{}
	if fetchedInfo != nil {
		publishTime = fetchedInfo.PublishTime
	}
	if publishTime.IsZero() {
		downloader := NewDownloader(api.config)
		if info, err := downloader.GetVideoInfo(video.URL); err == nil {
			publishTime = info.PublishTime
		}
	}

	feedVideo := FeedVideo{
		ID:          video.ID,
		Title:       video.Title,
		URL:         normalizeChannelVideoURL(video.ID),
		PublishedAt: publishTime,
		AddedAt:     time.Now().UTC(),
	}

	if err := api.storage.UpsertFeedVideo(channelID, feedVideo); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to add video under channel: %v", err))
		return
	}

	disablePruning := video.DisablePruning

	go func() {
		dl := NewDownloader(api.config)
		result, err := dl.DownloadVideo(feedVideo.URL, feedVideo.ID, channelName, quality, format, true, false)
		if err != nil {
			logScopef("channel", channelID, channelName, "Manually added video download failed for %s: %v", feedVideo.ID, err)
			if setErr := api.storage.SetChannelError(channelID, fmt.Sprintf("Manual add-video download of %q failed: %v", feedVideo.Title, err)); setErr != nil {
				log.Printf("Failed to set channel error after manual add-video download failure: %v", setErr)
			}
			return
		}
		if result != nil && result.Skipped {
			logScopef("channel", channelID, channelName, "Manually added video download skipped for %s: %s", feedVideo.ID, result.SkipReason)
			return
		}
		if err := api.storage.MarkVideoAsDownloaded(channelID, feedVideo.ID, feedVideo.Title, feedVideo.PublishedAt); err != nil {
			logScopef("channel", channelID, channelName, "Failed to mark manually added video %s as downloaded: %v", feedVideo.ID, err)
		}
		if disablePruning {
			if err := api.storage.UpdateChannelDownloadedVideoPruning(channelID, feedVideo.ID, true); err != nil {
				logScopef("channel", channelID, channelName, "Failed to set disable_pruning for manually added video %s: %v", feedVideo.ID, err)
			}
		}
		if err := api.storage.RemoveFeedVideo(channelID, feedVideo.ID); err != nil {
			logScopef("channel", channelID, channelName, "Failed to remove feed video %s after manual add-video download: %v", feedVideo.ID, err)
		}
		if result != nil && result.ChannelIcon != "" {
			if err := api.storage.SetChannelThumbnailIfEmpty(channelID, result.ChannelIcon); err != nil {
				logScopef("channel", channelID, channelName, "Failed to update thumbnail after manual add-video download: %v", err)
			}
		}
		logScopef("channel", channelID, channelName, "Manually added video downloaded under channel: %s (%s)", feedVideo.ID, feedVideo.Title)
	}()

	logScopef("channel", channelID, channelName, "Video added via API under tracked channel: %s (%s)", feedVideo.ID, feedVideo.Title)
	api.sendSuccess(w, map[string]string{"video_id": feedVideo.ID, "channel_id": channelID, "message": "Video added under existing channel"})
}

// handleVideoByID handles DELETE and PUT for a specific video
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
		logScopef("video", id, id, "Video removed via API: %s", id)
		api.sendSuccess(w, map[string]string{"id": id})
	case http.MethodPut:
		api.updateVideo(w, r, id)
	default:
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// updateVideo updates video settings (retention days, pruning, video quality, video format, shorts preference)
func (api *APIServer) updateVideo(w http.ResponseWriter, r *http.Request, id string) {
	var updateData struct {
		RetentionDays  int    `json:"retention_days"`
		DisablePruning bool   `json:"disable_pruning"`
		VideoQuality   string `json:"video_quality"`
		VideoFormat    string `json:"video_format"`
		DownloadShorts bool   `json:"download_shorts"`
	}

	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		api.sendError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := api.storage.UpdateVideo(id, updateData.RetentionDays, updateData.DisablePruning, updateData.VideoQuality, updateData.VideoFormat, true); err != nil {
		api.sendError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to update video: %v", err))
		return
	}

	logScopef("video", id, id, "Video updated via API: %s", id)
	api.sendSuccess(w, map[string]string{"id": id})
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
		"disable_pruning":          api.config.DisablePruning,
		"download_dir":             api.config.DownloadDir,
		"file_name_pattern":        api.config.FileNamePattern,
		"api_port":                 api.config.APIPort,
		"max_concurrent_downloads": api.config.MaxConcurrent,
		"default_video_format":     api.config.DefaultVideoFormat,
		"default_video_quality":    api.config.DefaultVideoQuality,
		"max_log_entries":          api.config.MaxLogEntries,
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
	if val, ok := updates["disable_pruning"].(bool); ok {
		api.config.DisablePruning = val
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
	if val, ok := updates["default_video_format"].(string); ok {
		api.config.DefaultVideoFormat = val
	}
	if val, ok := updates["default_video_quality"].(string); ok {
		api.config.DefaultVideoQuality = val
	}
	if val, ok := updates["max_log_entries"].(float64); ok {
		api.config.MaxLogEntries = int(val)
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

	if api.logs != nil {
		api.logs.SetMaxEntries(api.config.MaxLogEntries)
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
