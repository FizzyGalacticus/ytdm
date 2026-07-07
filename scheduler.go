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

// downloadKind distinguishes channel-sourced from standalone video download requests.
type downloadKind int

const (
	downloadKindChannel downloadKind = iota
	downloadKindVideo
)

// DownloadRequest carries everything the download worker needs to execute a single download.
type DownloadRequest struct {
	Kind       downloadKind
	VideoID    string
	VideoURL   string
	VideoTitle string

	// Fields used when Kind == downloadKindChannel
	ChannelID      string
	ChannelName    string
	Quality        string
	Format         string
	DownloadShorts bool
	PublishTime    time.Time

	// Fields used when Kind == downloadKindVideo (snapshot of the Video at enqueue time)
	VideoEntry *Video
}

// downloadExecFn is the type of function the download worker calls per request.
// Accepting it as a parameter lets tests substitute a fake implementation.
type downloadExecFn func(ctx context.Context, req DownloadRequest)

// runDownloadWorker is the single goroutine responsible for all video downloads.
// It deduplicates concurrent requests by VideoID and honours config.MaxConcurrent.
// Pass execFn == nil to use the real download implementation.
func runDownloadWorker(
	ctx context.Context,
	config *Config,
	storage *Storage,
	downloader *Downloader,
	queue <-chan DownloadRequest,
	execFn downloadExecFn,
) {
	if execFn == nil {
		execFn = func(ctx context.Context, req DownloadRequest) {
			executeDownload(ctx, req, storage, downloader)
		}
	}

	inFlight := map[string]bool{} // video IDs that are currently downloading
	activeCount := 0
	var pending []DownloadRequest
	done := make(chan string, 256) // receives videoID when a download goroutine finishes
	var wg sync.WaitGroup

	dispatch := func(req DownloadRequest) {
		activeCount++
		wg.Add(1)
		go func(r DownloadRequest) {
			defer wg.Done()
			execFn(ctx, r)
			select {
			case done <- r.VideoID:
			case <-ctx.Done():
			}
		}(req)
	}

	tryFlush := func() {
		config.RLock()
		max := config.MaxConcurrent
		config.RUnlock()
		if max <= 0 {
			max = 1
		}
		for len(pending) > 0 && activeCount < max {
			req := pending[0]
			pending = pending[1:]
			dispatch(req)
		}
	}

	log.Println("Download worker started")
	defer func() {
		wg.Wait()
		log.Println("Download worker stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case videoID := <-done:
			delete(inFlight, videoID)
			activeCount--
			tryFlush()
		case req, ok := <-queue:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				continue
			}
			if !inFlight[req.VideoID] {
				inFlight[req.VideoID] = true
				pending = append(pending, req)
				tryFlush()
			}
		}
	}
}

func executeDownload(ctx context.Context, req DownloadRequest, storage *Storage, downloader *Downloader) {
	switch req.Kind {
	case downloadKindChannel:
		executeChannelVideoDownload(ctx, req, storage, downloader)
	case downloadKindVideo:
		executeStandaloneVideoDownload(ctx, req, storage, downloader)
	}
}

func executeChannelVideoDownload(ctx context.Context, req DownloadRequest, storage *Storage, downloader *Downloader) {
	if !storage.HasChannel(req.ChannelID) || ctx.Err() != nil {
		return
	}

	logScopef("channel", req.ChannelID, req.ChannelName, "Attempting download for video %s (%s)", req.VideoID, req.VideoTitle)

	result, err := downloader.DownloadVideo(req.VideoURL, req.VideoID, req.ChannelName, req.Quality, req.Format, req.DownloadShorts, true)
	if err != nil {
		logScopef("channel", req.ChannelID, req.ChannelName, "Download failed for video %s (%s): %v", req.VideoID, req.VideoTitle, err)
		_ = storage.SetChannelError(req.ChannelID, fmt.Sprintf("download failed for %s: %v", req.VideoID, err))
		return
	}

	if result != nil && result.Skipped {
		if result.IsShort {
			logScopef("channel", req.ChannelID, req.ChannelName, "Filtered short video %s (%s): removing from feed", req.VideoID, req.VideoTitle)
			_ = storage.RemoveFeedVideo(req.ChannelID, req.VideoID)
			_ = storage.AddPrunedVideo(req.ChannelID, req.VideoID, req.PublishTime)
		} else if result.IsTooShort {
			logScopef("channel", req.ChannelID, req.ChannelName, "Video %s (%s) is under 2 minutes: flagging for manual download only", req.VideoID, req.VideoTitle)
			_ = storage.MarkFeedVideoManualOnly(req.ChannelID, req.VideoID)
		} else {
			logScopef("channel", req.ChannelID, req.ChannelName, "Skipped video %s (%s): %s", req.VideoID, req.VideoTitle, result.SkipReason)
		}
		return
	}

	if err := storage.MarkVideoAsDownloaded(req.ChannelID, req.VideoID, req.VideoTitle, req.PublishTime); err != nil {
		logScopef("channel", req.ChannelID, req.ChannelName, "Failed to mark video as downloaded: %v", err)
	}
	if err := storage.RemoveFeedVideo(req.ChannelID, req.VideoID); err != nil {
		logScopef("channel", req.ChannelID, req.ChannelName, "Failed to remove feed video after download: %v", err)
	}
	if result != nil && result.ChannelIcon != "" {
		_ = storage.SetChannelThumbnailIfEmpty(req.ChannelID, result.ChannelIcon)
	}
	_ = storage.ClearChannelError(req.ChannelID)
	logScopef("channel", req.ChannelID, req.ChannelName, "Downloaded video %s (%s)", req.VideoID, req.VideoTitle)
}

func executeStandaloneVideoDownload(ctx context.Context, req DownloadRequest, storage *Storage, downloader *Downloader) {
	if !storage.HasVideo(req.VideoID) || ctx.Err() != nil {
		return
	}

	// Use the snapshot from the request; fall back to a fresh storage read if absent.
	var vid Video
	if req.VideoEntry != nil {
		vid = *req.VideoEntry
	} else {
		v, ok := storage.GetVideo(req.VideoID)
		if !ok {
			return
		}
		vid = v
	}

	var channelName string
	var publishTime time.Time

	if vid.Uploader != "" {
		channelName = strings.TrimSpace(vid.Uploader)
	}
	if channelName == "" && vid.UploaderID != "" {
		channelName = strings.TrimSpace(vid.UploaderID)
	}

	if channelName == "" {
		videoInfo, liteErr := downloader.GetVideoInfoLite(vid.URL)
		if liteErr == nil {
			channelName = strings.TrimSpace(videoInfo.Uploader)
			if channelName == "" {
				channelName = strings.TrimSpace(videoInfo.UploaderID)
			}
			vid.Uploader = strings.TrimSpace(videoInfo.Uploader)
			vid.UploaderID = strings.TrimSpace(videoInfo.UploaderID)
		}
	}

	if channelName == "" {
		videoInfo, err := downloader.GetVideoInfo(vid.URL)
		if err != nil {
			logScopef("video", vid.ID, vid.Title, "Failed to resolve channel metadata for video %s: %v", vid.Title, err)
			_ = storage.SetVideoError(vid.ID, err.Error())
			return
		}
		channelName = strings.TrimSpace(videoInfo.Uploader)
		if channelName == "" {
			channelName = strings.TrimSpace(videoInfo.UploaderID)
		}
		publishTime = videoInfo.PublishTime
		vid.Uploader = strings.TrimSpace(videoInfo.Uploader)
		vid.UploaderID = strings.TrimSpace(videoInfo.UploaderID)
	}

	if channelName == "" {
		logScopef("video", vid.ID, vid.Title, "Failed to determine channel name for video %s", vid.Title)
		_ = storage.SetVideoError(vid.ID, fmt.Sprintf("could not determine channel name for video %s", vid.Title))
		return
	}

	if !storage.HasVideo(vid.ID) {
		return
	}

	logScopef("video", vid.ID, vid.Title, "Attempting download for video: %s", vid.Title)

	precheckedVideoID := extractYouTubeVideoID(vid.URL)
	result, err := downloader.DownloadVideo(vid.URL, precheckedVideoID, channelName, vid.VideoQuality, vid.VideoFormat, vid.DownloadShorts, false)
	if err != nil {
		logScopef("video", vid.ID, vid.Title, "Failed to download video %s: %v", vid.Title, err)
		_ = storage.SetVideoError(vid.ID, err.Error())
		return
	}

	if result != nil && result.Skipped {
		if result.SkipReason != "" {
			logScopef("video", vid.ID, vid.Title, "Finished video %s (skipped: %s)", vid.Title, result.SkipReason)
		} else {
			logScopef("video", vid.ID, vid.Title, "Finished video %s (skipped)", vid.Title)
		}
		return
	}

	downloadedVideoID := strings.TrimSpace(precheckedVideoID)
	if result != nil && strings.TrimSpace(result.VideoID) != "" {
		downloadedVideoID = strings.TrimSpace(result.VideoID)
	}
	if downloadedVideoID == "" {
		logScopef("video", vid.ID, vid.Title, "Failed to record video %s: could not determine downloaded video ID", vid.Title)
		_ = storage.SetVideoError(vid.ID, fmt.Sprintf("could not determine downloaded video ID for %s", vid.Title))
		return
	}

	downloadedTitle := strings.TrimSpace(vid.Title)
	if result != nil && strings.TrimSpace(result.VideoTitle) != "" {
		downloadedTitle = strings.TrimSpace(result.VideoTitle)
	}
	if downloadedTitle == "" {
		downloadedTitle = downloadedVideoID
	}

	if err := storage.MarkVideoAsDownloaded(vid.ID, downloadedVideoID, downloadedTitle, publishTime); err != nil {
		logScopef("video", vid.ID, vid.Title, "Failed to mark video %s as downloaded: %v", vid.Title, err)
		_ = storage.SetVideoError(vid.ID, err.Error())
		return
	}

	if vid.Uploader != "" || vid.UploaderID != "" {
		if err := storage.UpdateVideoUploaderInfo(vid.ID, vid.Uploader, vid.UploaderID); err != nil {
			logScopef("video", vid.ID, vid.Title, "Failed to cache uploader info for video %s: %v", vid.Title, err)
		}
	}

	_ = storage.ClearVideoError(vid.ID)
	logScopef("video", vid.ID, vid.Title, "Finished video %s (downloaded)", vid.Title)
}

// runChannelMonitor is a long-running goroutine for a single channel.
// It checks the feed on the configured interval and sends download requests to downloadQueue.
// It exits when its context is cancelled or the channel is deleted from storage.
func runChannelMonitor(ctx context.Context, channelID string, config *Config, storage *Storage, downloader *Downloader, downloadQueue chan<- DownloadRequest) {
	ch, ok := storage.GetChannel(channelID)
	if !ok {
		return
	}
	logScopef("channel", channelID, ch.Name, "Channel monitor started")
	defer logScopef("channel", channelID, ch.Name, "Channel monitor stopped")

	// Run initial check immediately so newly-added channels are acted on right away.
	if !checkAndQueueChannel(ctx, channelID, storage, downloader, downloadQueue) {
		return
	}

	lastInterval := config.GetCheckInterval()
	ticker := time.NewTicker(lastInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !storage.HasChannel(channelID) {
				return
			}
			if interval := config.GetCheckInterval(); interval > 0 && interval != lastInterval {
				ticker.Stop()
				ticker = time.NewTicker(interval)
				lastInterval = interval
			}
			checkAndQueueChannel(ctx, channelID, storage, downloader, downloadQueue)
		}
	}
}

// channelNeedsInitialBacklogScan reports whether ch has never successfully completed a
// feed scan before, in which case discovery should use the wide cutoff-based window
// instead of the narrower retention-based one. This is deliberately based on
// BacklogScanComplete (stamped once, only after a scan succeeds, never reverts) rather
// than on:
//   - DownloadedVideos/PrunedVideos being empty: those lists can legitimately empty back
//     out via retention trimming, and re-deriving "new" from their length would cause the
//     wide backlog window to re-open, rediscovering videos whose pruned/dismissed record
//     was just trimmed away.
//   - LastChecked: it is stamped on every scan attempt, including failed ones, so using
//     it here would let a transient RSS/yt-dlp failure on a channel's very first scan
//     permanently burn the one-time wide-window opportunity before it ever succeeded.
func channelNeedsInitialBacklogScan(ch Channel) bool {
	return !ch.BacklogScanComplete
}

// checkAndQueueChannel performs one feed-check cycle for channelID: fetches new videos,
// updates FeedVideos in storage, and sends DownloadRequests to downloadQueue.
// Returns false only when the channel no longer exists in storage.
func checkAndQueueChannel(ctx context.Context, channelID string, storage *Storage, downloader *Downloader, downloadQueue chan<- DownloadRequest) bool {
	ch, ok := storage.GetChannel(channelID)
	if !ok {
		return false
	}

	effectiveRetention := EffectiveRetentionDays(ch.RetentionDays, getDefaultRetentionDays(downloader))
	logScopef("channel", ch.ID, ch.Name, "Processing channel: %s (retention: %d days)", ch.Name, effectiveRetention)

	defer func() {
		if ctx.Err() != nil {
			return
		}
		if err := storage.UpdateChannelLastChecked(ch.ID, time.Now()); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to update channel last checked time: %v", err)
		}
	}()

	// Lazy-populate channel thumbnail from existing info.json files if not yet set.
	if ch.ThumbnailURL == "" {
		if icon := downloader.GetChannelThumbnailForChannel(ch.Name); icon != "" {
			if err := storage.SetChannelThumbnailIfEmpty(ch.ID, icon); err != nil {
				logScopef("channel", ch.ID, ch.Name, "Failed to set channel thumbnail: %v", err)
			}
		}
	}

	// Discovery window: for a channel that has never been scanned before, with a cutoff
	// date set, use the cutoff so the initial backlog is fetched. For channels that have
	// been scanned at least once, use max(cutoff, retention) so the window stays bounded.
	var since time.Time
	isNewChannel := channelNeedsInitialBacklogScan(ch)
	if isNewChannel && !ch.CutoffDate.IsZero() {
		since = NormalizeToUTC(ch.CutoffDate).Add(-time.Second)
	} else {
		since = BuildChannelSinceTime(time.Now(), effectiveRetention, ch.CutoffDate)
	}
	if since.IsZero() {
		logScopef("channel", ch.ID, ch.Name, "Checking channel feed for new videos (since: none)")
	} else {
		logScopef("channel", ch.ID, ch.Name, "Checking channel feed for new videos (since: %s)", since.Format(time.RFC3339))
		if err := storage.TrimPrunedVideos(ch.ID, since); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to trim pruned video list: %v", err)
		}
		if err := storage.PruneFeedVideos(ch.ID, since); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to prune feed videos: %v", err)
		}
	}

	// Always try fast index (RSS) first, fall back to yt-dlp.
	var allFeedVideos []VideoInfo
	feedSource := "rss"
	var feedErr error
	allFeedVideos, feedErr = downloader.GetChannelVideosFromRSS(ch.ID, ch.URL, since)
	if feedErr != nil {
		logScopef("channel", ch.ID, ch.Name, "RSS feed lookup failed, falling back to yt-dlp: %v", feedErr)
		feedSource = "yt-dlp"
		allFeedVideos, feedErr = downloader.GetChannelVideos(ch.URL, since, ch.DownloadShorts)
	}
	if feedErr != nil {
		_ = storage.SetChannelError(ch.ID, feedErr.Error())
		return true
	}

	// The feed fetch succeeded (regardless of what it found), so the one-time wide
	// backlog window has served its purpose and should not reopen on later scans.
	if isNewChannel {
		if err := storage.MarkChannelBacklogScanComplete(ch.ID); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to mark backlog scan complete: %v", err)
		}
	}

	// Retroactively patch IsShort on any existing FeedVideos the RSS scan identified as shorts.
	shortIDsInFeed := make(map[string]bool, len(allFeedVideos))
	for _, v := range allFeedVideos {
		if v.IsShort {
			shortIDsInFeed[v.ID] = true
		}
	}
	if freshCh, ok := storage.GetChannel(ch.ID); ok {
		for _, fv := range freshCh.FeedVideos {
			isKnownShort := fv.IsShort || shortIDsInFeed[fv.ID]
			if isKnownShort && !ch.DownloadShorts {
				if err := storage.RemoveFeedVideo(ch.ID, fv.ID); err != nil {
					logScopef("channel", ch.ID, ch.Name, "Failed to remove short feed video %s: %v", fv.ID, err)
				}
				if err := storage.AddPrunedVideo(ch.ID, fv.ID, fv.PublishedAt); err != nil {
					logScopef("channel", ch.ID, ch.Name, "Failed to prune short video %s: %v", fv.ID, err)
				}
			} else if shortIDsInFeed[fv.ID] && !fv.IsShort {
				patched := fv
				patched.IsShort = true
				if err := storage.UpsertFeedVideo(ch.ID, patched); err != nil {
					logScopef("channel", ch.ID, ch.Name, "Failed to update short flag on feed video %s: %v", fv.ID, err)
				}
			}
		}
	}

	// Filter by the channel's shorts preference before any further processing.
	var candidates []VideoInfo
	for _, v := range allFeedVideos {
		if !ch.DownloadShorts && v.IsShort {
			continue
		}
		candidates = append(candidates, v)
	}

	logScopef("channel", ch.ID, ch.Name, "Feed check complete via %s: discovered %d candidate videos", feedSource, len(candidates))

	// Filter to videos not already downloaded.
	var videosToDownload []VideoInfo
	skippedCount := 0
	for _, video := range candidates {
		if !storage.IsVideoDownloaded(ch.ID, video.ID) {
			videosToDownload = append(videosToDownload, video)
		} else {
			skippedCount++
		}
	}
	logScopef("channel", ch.ID, ch.Name, "Eligibility result: %d to download, %d already tracked/skipped", len(videosToDownload), skippedCount)

	// One-time migration for channels upgraded from a schema without pruned_videos.
	// Treat videos published before the most-recent tracked download as already-pruned so
	// we don't re-attempt content that has already been processed.
	if ch.PrunedVideos == nil && len(videosToDownload) > 0 {
		var latestPublish time.Time
		for _, dv := range ch.DownloadedVideos {
			if dv.PublishDate.After(latestPublish) {
				latestPublish = dv.PublishDate
			}
		}
		var migrateVideos []PrunedVideo
		var fresh []VideoInfo
		for _, video := range videosToDownload {
			if !latestPublish.IsZero() && video.PublishTime.Before(latestPublish) {
				migrateVideos = append(migrateVideos, PrunedVideo{ID: video.ID, PublishDate: video.PublishTime})
				skippedCount++
			} else {
				fresh = append(fresh, video)
			}
		}
		if len(migrateVideos) > 0 {
			logScopef("channel", ch.ID, ch.Name, "One-time migration: marking %d previously-seen videos as pruned (pre-dates latest tracked download)", len(migrateVideos))
		}
		if err := storage.MigratePrunedVideos(ch.ID, migrateVideos); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Migration error: %v", err)
		}
		videosToDownload = fresh
	}

	// Track all newly-discovered videos in FeedVideos so they are visible in the UI
	// regardless of whether auto-download is enabled.
	for _, video := range videosToDownload {
		fv := FeedVideo{
			ID:          video.ID,
			Title:       video.Title,
			URL:         normalizeChannelVideoURL(video.ID),
			PublishedAt: video.PublishTime,
			AddedAt:     time.Now(),
			IsShort:     video.IsShort,
		}
		if err := storage.UpsertFeedVideo(ch.ID, fv); err != nil {
			logScopef("channel", ch.ID, ch.Name, "Failed to track feed video %s: %v", video.ID, err)
		}
	}

	if len(videosToDownload) == 0 {
		_ = storage.ClearChannelError(ch.ID)
		doChannelCleanup(ctx, ch, storage, downloader)
		return true
	}

	// Build the manual-download-only set from fresh storage state.
	manualDownloadOnly := make(map[string]bool)
	if refreshed, ok := storage.GetChannel(ch.ID); ok {
		for _, fv := range refreshed.FeedVideos {
			if fv.ManualDownloadOnly {
				manualDownloadOnly[fv.ID] = true
			}
		}
	}

	if ch.SkipAutoDownload {
		logScopef("channel", ch.ID, ch.Name, "Auto-download disabled: %d video(s) tracked in feed, skipping download", len(videosToDownload))
		doChannelCleanup(ctx, ch, storage, downloader)
		return true
	}

	// Enqueue each eligible video for the download worker.
	for _, video := range videosToDownload {
		if !storage.HasChannel(ch.ID) {
			return false
		}
		if manualDownloadOnly[video.ID] {
			logScopef("channel", ch.ID, ch.Name, "Skipping auto-download for video %s (%s): manual download only (too short)", video.ID, video.Title)
			continue
		}

		req := DownloadRequest{
			Kind:           downloadKindChannel,
			VideoID:        video.ID,
			VideoURL:       normalizeChannelVideoURL(video.ID),
			VideoTitle:     video.Title,
			ChannelID:      ch.ID,
			ChannelName:    ch.Name,
			Quality:        ch.VideoQuality,
			Format:         ch.VideoFormat,
			DownloadShorts: ch.DownloadShorts,
			PublishTime:    video.PublishTime,
		}

		select {
		case downloadQueue <- req:
			logScopef("channel", ch.ID, ch.Name, "Queued download for video %s (%s)", video.ID, video.Title)
		case <-ctx.Done():
			return true
		}
	}

	_ = storage.ClearChannelError(ch.ID)
	doChannelCleanup(ctx, ch, storage, downloader)
	return true
}

// doChannelCleanup runs retention cleanup for a single channel.
func doChannelCleanup(ctx context.Context, ch Channel, storage *Storage, downloader *Downloader) {
	if ctx.Err() != nil || ch.DisablePruning || configPruningDisabled(downloader) {
		return
	}
	retentionDays := EffectiveRetentionDays(ch.RetentionDays, getDefaultRetentionDays(downloader))
	if retentionDays <= 0 {
		return
	}
	if err := downloader.CleanOldVideosForChannel(ch.Name, ch.ID, retentionDays, ch.CutoffDate, storage); err != nil {
		logScopef("channel", ch.ID, ch.Name, "Error cleaning old videos for channel %s: %v", ch.Name, err)
	}
}

// runVideoMonitor is a single goroutine that checks all standalone videos on every interval
// tick and whenever a wakeup signal is received. Receiving on wakeup is how the manager
// notifies the monitor that a new video was just added so it can be queued immediately.
func runVideoMonitor(ctx context.Context, config *Config, storage *Storage, downloader *Downloader, downloadQueue chan<- DownloadRequest, wakeup <-chan struct{}) {
	log.Println("Video monitor started")
	defer log.Println("Video monitor stopped")

	// Run an initial check immediately so any existing pending videos are queued on startup.
	checkAndQueueAllVideos(ctx, storage, downloader, downloadQueue)

	lastInterval := config.GetCheckInterval()
	ticker := time.NewTicker(lastInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-wakeup:
			checkAndQueueAllVideos(ctx, storage, downloader, downloadQueue)
		case <-ticker.C:
			if interval := config.GetCheckInterval(); interval > 0 && interval != lastInterval {
				ticker.Stop()
				ticker = time.NewTicker(interval)
				lastInterval = interval
			}
			checkAndQueueAllVideos(ctx, storage, downloader, downloadQueue)
		}
	}
}

// checkAndQueueAllVideos iterates all standalone videos, queuing downloads for any that
// have not yet been downloaded and running retention cleanup for those that have.
func checkAndQueueAllVideos(ctx context.Context, storage *Storage, downloader *Downloader, downloadQueue chan<- DownloadRequest) {
	if ctx.Err() != nil {
		return
	}
	for _, vid := range storage.GetVideos() {
		if ctx.Err() != nil {
			return
		}
		checkAndQueueVideo(ctx, vid.ID, storage, downloader, downloadQueue)
	}
}

// checkAndQueueVideo performs one check cycle for a standalone video.
// If not yet downloaded, it enqueues a download request.
// If already downloaded, it runs retention cleanup.
// Returns false only when the video no longer exists in storage (deleted or pruned).
func checkAndQueueVideo(ctx context.Context, videoID string, storage *Storage, downloader *Downloader, downloadQueue chan<- DownloadRequest) bool {
	vid, ok := storage.GetVideo(videoID)
	if !ok {
		return false
	}

	if ctx.Err() != nil {
		return true
	}

	defer func() {
		if ctx.Err() != nil {
			return
		}
		_ = storage.UpdateVideoLastChecked(vid.ID, time.Now())
	}()

	// Already downloaded: run retention cleanup.
	if len(vid.DownloadedVideos) > 0 {
		if doVideoCleanup(ctx, vid, storage, downloader) {
			return false // removed from storage; tell monitor to exit
		}
		return true
	}

	// Not yet downloaded: enqueue for the download worker.
	entry := vid
	req := DownloadRequest{
		Kind:       downloadKindVideo,
		VideoID:    vid.ID,
		VideoURL:   vid.URL,
		VideoTitle: vid.Title,
		VideoEntry: &entry,
	}

	select {
	case downloadQueue <- req:
	case <-ctx.Done():
	}

	return true
}

// doVideoCleanup runs retention cleanup for a standalone video.
// Returns true if the video was removed from storage (the monitor should exit).
func doVideoCleanup(ctx context.Context, vid Video, storage *Storage, downloader *Downloader) bool {
	if ctx.Err() != nil || vid.DisablePruning || configPruningDisabled(downloader) {
		return false
	}
	retentionDays := EffectiveRetentionDays(vid.RetentionDays, getDefaultRetentionDays(downloader))
	if retentionDays <= 0 {
		return false
	}
	removed, err := downloader.CleanOldVideosForVideo(vid.Title, vid.ID, retentionDays, storage)
	if err != nil {
		logScopef("video", vid.ID, vid.Title, "Error cleaning old videos for video %s: %v", vid.Title, err)
		return false
	}
	if removed {
		if err := storage.RemoveVideo(vid.ID); err != nil {
			logScopef("video", vid.ID, vid.Title, "Error removing pruned video entry %s: %v", vid.Title, err)
			return false
		}
		return true
	}
	return false
}

// RunScheduler starts the per-channel and per-video monitor goroutines and the download
// worker. It detects channel/video additions and deletions and starts or cancels the
// corresponding goroutines in response.
func RunScheduler(ctx context.Context, config *Config, storage *Storage) {
	downloader := NewDownloader(config)
	log.Println("Scheduler started")

	downloadQueue := make(chan DownloadRequest, 256)

	// videoWakeup lets the manager nudge the video monitor immediately when storage changes.
	videoWakeup := make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDownloadWorker(ctx, config, storage, downloader, downloadQueue, nil)
	}()

	// Single video monitor goroutine covers all standalone videos.
	wg.Add(1)
	go func() {
		defer wg.Done()
		runVideoMonitor(ctx, config, storage, downloader, downloadQueue, videoWakeup)
	}()

	type workerEntry struct{ cancel context.CancelFunc }
	channelWorkers := map[string]workerEntry{}

	syncChannelWorkers := func() {
		channels := storage.GetChannels()

		// Start goroutines for newly-added channels.
		current := make(map[string]bool, len(channels))
		for _, ch := range channels {
			current[ch.ID] = true
			if _, exists := channelWorkers[ch.ID]; !exists {
				cctx, cancel := context.WithCancel(ctx)
				channelWorkers[ch.ID] = workerEntry{cancel}
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					runChannelMonitor(cctx, id, config, storage, downloader, downloadQueue)
				}(ch.ID)
			}
		}
		// Cancel goroutines for deleted channels.
		for id, e := range channelWorkers {
			if !current[id] {
				e.cancel()
				delete(channelWorkers, id)
			}
		}
	}

	// Reconcile storage with disk periodically (at most once per check interval).
	var lastReconcile time.Time
	reconcile := func() {
		if interval := config.GetCheckInterval(); time.Since(lastReconcile) < interval {
			return
		}
		lastReconcile = time.Now()
		config.RLock()
		dir := config.DownloadDir
		config.RUnlock()
		if err := storage.ReconcileDownloadedVideos(dir); err != nil {
			log.Printf("Error reconciling downloaded video entries: %v", err)
		}
	}

	syncChannelWorkers()

	manageTicker := time.NewTicker(5 * time.Second)
	defer manageTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Scheduler stopping...")
			for _, e := range channelWorkers {
				e.cancel()
			}
			wg.Wait()
			log.Println("Scheduler stopped")
			return
		case <-manageTicker.C:
			syncChannelWorkers()
			reconcile()
		case <-storage.NotifyCh():
			syncChannelWorkers()
			// Wake up the video monitor so newly-added videos are queued immediately.
			select {
			case videoWakeup <- struct{}{}:
			default:
			}
		}
	}
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
