package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Channel discovery fallback for when YouTube's RSS feed is unavailable (it has been
// answering 404 for many channels since October 2026).
//
// A flat listing of the channel is one cheap request that returns every tab (Videos,
// Live, Shorts) newest-first, but without upload dates. Dates are needed for the
// cutoff/retention window, so only videos ytdm has never seen are fully extracted, and
// each tab is walked only until the first video older than the window.

const (
	// channelListingLimit is how many entries per tab the flat listing returns.
	channelListingLimit = 30
	// channelListingMaxExtractions bounds the per-check cost of a big initial backlog;
	// the rest is picked up on later checks.
	channelListingMaxExtractions = 50
	// unavailableVideoRetryAfter is how long a video that failed to extract (members-
	// only, private, region-locked, ...) is skipped before being tried again.
	unavailableVideoRetryAfter = 6 * time.Hour
)

// flatListingEntry is a node of yt-dlp's --flat-playlist -J output: the channel, one of
// its tabs, or a video. Tabs and channels have Entries; videos don't.
type flatListingEntry struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	URL          string             `json:"url"`
	Availability string             `json:"availability"`
	LiveStatus   string             `json:"live_status"`
	Entries      []flatListingEntry `json:"entries"`
}

// listingTabs splits a listing into its tabs, each newest-first. A listing of a single
// tab (e.g. a /videos URL) has the videos directly under the root.
func listingTabs(root flatListingEntry) [][]flatListingEntry {
	var tabs [][]flatListingEntry
	var videos []flatListingEntry
	for _, entry := range root.Entries {
		if entry.Entries != nil {
			tabs = append(tabs, entry.Entries)
			continue
		}
		videos = append(videos, entry)
	}
	if len(videos) > 0 {
		tabs = append(tabs, videos)
	}
	return tabs
}

// ChannelListingStats summarizes a listing-based discovery for logging.
type ChannelListingStats struct {
	Listed      int // videos in the flat listing
	Extracted   int // videos fully extracted to learn their upload date
	Unavailable int // videos skipped because they couldn't be extracted (now or recently)
}

// listingMemory remembers, across checks, what extraction learned about videos that
// don't end up tracked anywhere, so they aren't re-extracted on every check: upload
// dates of videos outside the discovery window, and videos that failed to extract.
type listingMemory struct {
	mu               sync.Mutex
	outsideWindow    map[string]time.Time
	unavailableUntil map[string]time.Time
}

var channelListingMemory = &listingMemory{
	outsideWindow:    map[string]time.Time{},
	unavailableUntil: map[string]time.Time{},
}

func (m *listingMemory) knownDate(videoID string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	date, ok := m.outsideWindow[videoID]
	return date, ok
}

func (m *listingMemory) rememberOutsideWindow(videoID string, date time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outsideWindow[videoID] = date
}

func (m *listingMemory) isUnavailable(videoID string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.unavailableUntil[videoID]
	if ok && !now.Before(until) {
		delete(m.unavailableUntil, videoID)
		return false
	}
	return ok
}

func (m *listingMemory) rememberUnavailable(videoID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unavailableUntil[videoID] = now.Add(unavailableVideoRetryAfter)
}

// GetChannelVideosFromListing discovers a channel's videos published after since, from
// a flat listing of channelURL. pending holds videos already discovered but not yet
// downloaded; they are returned from storage without extraction so they keep getting
// queued. handled reports videos already downloaded or pruned, which are skipped.
func (d *Downloader) GetChannelVideosFromListing(channelURL string, since time.Time, downloadShorts bool, pending map[string]VideoInfo, handled func(videoID string) bool) ([]VideoInfo, ChannelListingStats, error) {
	return d.getChannelVideosFromListing(channelListingMemory, time.Now(), channelURL, since, downloadShorts, pending, handled)
}

func (d *Downloader) getChannelVideosFromListing(memory *listingMemory, now time.Time, channelURL string, since time.Time, downloadShorts bool, pending map[string]VideoInfo, handled func(videoID string) bool) ([]VideoInfo, ChannelListingStats, error) {
	var stats ChannelListingStats
	stdout, stderr, err := d.runYtDlpWithCookieRetry([]string{
		"--flat-playlist",
		"--dump-single-json",
		"--playlist-end", fmt.Sprint(channelListingLimit),
		channelURL,
	})
	// A non-zero exit with a listing is a partial success (e.g. one tab failed).
	if err != nil && stdout.Len() == 0 {
		return nil, stats, fmt.Errorf("yt-dlp channel listing failed: %v, stderr: %s", err, stderr.String())
	}

	var root flatListingEntry
	if err := json.Unmarshal(stdout.Bytes(), &root); err != nil {
		return nil, stats, fmt.Errorf("failed to parse channel listing: %v", err)
	}

	d.config.RLock()
	hasCookies := d.config.YtDlp.CookiesBrowser != "" || d.config.YtDlp.CookiesFile != ""
	d.config.RUnlock()

	var videos []VideoInfo
	seen := map[string]bool{}
	for _, tab := range listingTabs(root) {
	entries:
		for _, entry := range tab {
			stats.Listed++
			isShort := strings.Contains(entry.URL, "/shorts/")
			switch {
			case entry.ID == "" || seen[entry.ID] || (isShort && !downloadShorts) || handled(entry.ID):
				continue
			// Not downloadable yet; picked up once it has aired.
			case entry.LiveStatus == "is_upcoming" || entry.LiveStatus == "is_live":
				continue
			}

			seen[entry.ID] = true

			if info, ok := pending[entry.ID]; ok {
				if !info.PublishTime.After(since) {
					break entries
				}
				info.IsShort = info.IsShort || isShort
				videos = append(videos, info)
				continue
			}

			if date, ok := memory.knownDate(entry.ID); ok && !date.After(since) {
				break entries
			}

			// Members-only videos can't be extracted without (a member's) cookies.
			if memory.isUnavailable(entry.ID, now) || (entry.Availability == "subscriber_only" && !hasCookies) {
				stats.Unavailable++
				continue
			}
			if stats.Extracted >= channelListingMaxExtractions {
				return videos, stats, nil
			}

			stats.Extracted++
			info, err := d.GetVideoInfo(normalizeChannelVideoURL(entry.ID))
			if err != nil || info.PublishTime.IsZero() {
				memory.rememberUnavailable(entry.ID, now)
				stats.Unavailable++
				continue
			}
			if !info.PublishTime.After(since) {
				memory.rememberOutsideWindow(entry.ID, info.PublishTime)
				break entries
			}

			info.ID = entry.ID
			info.IsShort = isShort || isShortYouTubeURL(info.WebpageURL)
			if info.Title == "" {
				info.Title = entry.Title
			}
			videos = append(videos, *info)
		}
	}

	return videos, stats, nil
}
