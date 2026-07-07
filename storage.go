package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PrunedVideo records a video that was downloaded and then pruned, along with its
// publish date so that expired entries can be evicted from the list over time.
type PrunedVideo struct {
	ID          string    `json:"id"`
	PublishDate time.Time `json:"publish_date"`
}

// FeedVideo records a video seen in the channel's RSS feed that falls within the
// retention window but has not yet been downloaded.
type FeedVideo struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	URL                string    `json:"url"`
	PublishedAt        time.Time `json:"published_at"`
	AddedAt            time.Time `json:"added_at"`
	IsShort            bool      `json:"is_short,omitempty"`
	ManualDownloadOnly bool      `json:"manual_download_only,omitempty"` // set when duration < 2 min; skip auto-download
}

// DownloadedVideo tracks a downloaded video with its download date
type DownloadedVideo struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	DownloadDate   time.Time `json:"download_date"`
	PublishDate    time.Time `json:"publish_date"`
	DisablePruning bool      `json:"disable_pruning"`
}

// Channel represents a YouTube channel to monitor
type Channel struct {
	ID               string            `json:"id"`
	URL              string            `json:"url"`
	Name             string            `json:"name"`
	LastChecked      time.Time         `json:"last_checked"`
	RetentionDays    int               `json:"retention_days"`
	DisablePruning   bool              `json:"disable_pruning"`
	CutoffDate       time.Time         `json:"cutoff_date"`           // Don't download videos published before this date
	VideoQuality     string            `json:"video_quality"`         // Video quality preference (e.g., "best", "720", "480", "360")
	VideoFormat      string            `json:"video_format"`          // Video format preference (e.g., "mp4", "webm", "mkv")
	DownloadShorts   bool              `json:"download_shorts"`       // Whether to download short-format videos
	PrunedVideos     []PrunedVideo     `json:"pruned_videos"`         // Videos already downloaded then pruned; prevents re-download loops
	DownloadedVideos []DownloadedVideo `json:"downloaded_videos"`     // Track which videos have been downloaded with dates
	FeedVideos       []FeedVideo       `json:"feed_videos,omitempty"` // Videos seen in feed but not yet downloaded
	SkipAutoDownload bool              `json:"skip_auto_download"`    // When true, track feed videos but do not download automatically
	LastError        string            `json:"last_error,omitempty"`  // Most recent error message
	LastErrorTime    time.Time         `json:"last_error_time,omitempty"`
	ThumbnailURL     string            `json:"thumbnail_url,omitempty"` // Channel icon URL
	// BacklogScanComplete is set once a feed scan has successfully fetched data at least
	// once. Discovery uses the wide cutoff-based window only while this is false; it must
	// not be inferred from DownloadedVideos/PrunedVideos being empty (both can legitimately
	// empty back out via retention trimming) and must not be set on a failed scan attempt
	// (which would burn the one-time backlog window before it ever succeeded).
	BacklogScanComplete bool `json:"backlog_scan_complete,omitempty"`
}

// Video represents a specific YouTube video to monitor
type Video struct {
	ID               string            `json:"id"`
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	AddedDate        time.Time         `json:"added_date"`
	LastChecked      time.Time         `json:"last_checked"`
	RetentionDays    int               `json:"retention_days"`
	DisablePruning   bool              `json:"disable_pruning"`
	VideoQuality     string            `json:"video_quality"`         // Video quality preference (e.g., "best", "720", "480", "360")
	VideoFormat      string            `json:"video_format"`          // Video format preference (e.g., "mp4", "webm", "mkv")
	DownloadShorts   bool              `json:"download_shorts"`       // Whether to download short-format videos
	Uploader         string            `json:"uploader,omitempty"`    // Cached uploader/channel name to avoid re-querying API
	UploaderID       string            `json:"uploader_id,omitempty"` // Cached uploader ID to avoid re-querying API
	DownloadedVideos []DownloadedVideo `json:"downloaded_videos"`     // Track which videos have been downloaded with dates
	LastError        string            `json:"last_error,omitempty"`  // Most recent error message
	LastErrorTime    time.Time         `json:"last_error_time,omitempty"`
}

// StorageData holds all persisted data
type StorageData struct {
	Channels []Channel `json:"channels"`
	Videos   []Video   `json:"videos"`
}

// Storage manages persistent data with thread-safe operations
type Storage struct {
	mu       sync.RWMutex
	filePath string
	data     StorageData
	notifyCh chan struct{}
}

// NewStorage creates a new Storage instance
func NewStorage(filePath string) (*Storage, error) {
	s := &Storage{
		filePath: filePath,
		data:     StorageData{Channels: []Channel{}, Videos: []Video{}},
		notifyCh: make(chan struct{}, 16),
	}

	// Try to load existing data
	if err := s.load(); err != nil {
		// If file doesn't exist, create it with empty data
		if os.IsNotExist(err) {
			if err := s.save(); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return s, nil
}

// load reads data from the file
func (s *Storage) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, &s.data)
}

// save writes data to the file
func (s *Storage) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.filePath, data, 0644)
}

// notify signals that storage data has changed (non-blocking).
func (s *Storage) notify() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

// NotifyCh returns a channel that receives a value whenever channels or videos
// are added or removed, allowing the scheduler to react immediately.
func (s *Storage) NotifyCh() <-chan struct{} {
	return s.notifyCh
}

// GetChannels returns all channels
func (s *Storage) GetChannels() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()

	channels := make([]Channel, len(s.data.Channels))
	copy(channels, s.data.Channels)
	return channels
}

// GetChannel returns the channel with the given ID, if it exists.
func (s *Storage) GetChannel(id string) (Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ch := range s.data.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return Channel{}, false
}

// HasChannel returns true if a channel with the given ID exists
func (s *Storage) HasChannel(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ch := range s.data.Channels {
		if ch.ID == id {
			return true
		}
	}

	return false
}

// AddChannel adds a new channel
func (s *Storage) AddChannel(channel Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.data.Channels = append(s.data.Channels, channel)
	if err := s.save(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// RemoveChannel removes a channel by ID
func (s *Storage) RemoveChannel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, ch := range s.data.Channels {
		if ch.ID == id {
			s.data.Channels = append(s.data.Channels[:i], s.data.Channels[i+1:]...)
			if err := s.save(); err != nil {
				return err
			}
			s.notify()
			return nil
		}
	}

	return nil
}

// UpdateChannelLastChecked updates the last checked time for a channel
func (s *Storage) UpdateChannelLastChecked(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].LastChecked = t
			return s.save()
		}
	}

	return nil
}

// MarkChannelBacklogScanComplete records that channelID has successfully fetched feed
// data at least once, so future scans use the bounded retention window instead of the
// wide cutoff-based backlog window. Only call this after a scan actually succeeds.
func (s *Storage) MarkChannelBacklogScanComplete(channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == channelID {
			if s.data.Channels[i].BacklogScanComplete {
				return nil
			}
			s.data.Channels[i].BacklogScanComplete = true
			return s.save()
		}
	}

	return nil
}

// UpdateChannel updates retention days, pruning behavior, cutoff date, video quality, video format, and shorts preference for a channel
func (s *Storage) UpdateChannel(id string, retentionDays int, disablePruning bool, cutoffDate time.Time, videoQuality, videoFormat string, downloadShorts, skipAutoDownload bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].RetentionDays = retentionDays
			s.data.Channels[i].DisablePruning = disablePruning
			s.data.Channels[i].CutoffDate = cutoffDate
			s.data.Channels[i].VideoQuality = videoQuality
			s.data.Channels[i].VideoFormat = videoFormat
			s.data.Channels[i].DownloadShorts = downloadShorts
			s.data.Channels[i].SkipAutoDownload = skipAutoDownload
			return s.save()
		}
	}

	return nil
}

// UpsertFeedVideo adds or updates a video in the channel's FeedVideos list.
// ManualDownloadOnly is preserved from the existing entry so that a duration-based
// skip flag is not overwritten when the RSS feed re-discovers the same video.
func (s *Storage) UpsertFeedVideo(channelID string, video FeedVideo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		for j, fv := range s.data.Channels[i].FeedVideos {
			if fv.ID == video.ID {
				if fv.ManualDownloadOnly {
					video.ManualDownloadOnly = true
				}
				s.data.Channels[i].FeedVideos[j] = video
				return s.save()
			}
		}
		s.data.Channels[i].FeedVideos = append(s.data.Channels[i].FeedVideos, video)
		return s.save()
	}
	return nil
}

// MarkFeedVideoManualOnly sets ManualDownloadOnly=true on a feed video entry,
// indicating it should not be auto-downloaded (e.g. because it's under 2 minutes).
func (s *Storage) MarkFeedVideoManualOnly(channelID, videoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		for j, fv := range s.data.Channels[i].FeedVideos {
			if fv.ID == videoID {
				if s.data.Channels[i].FeedVideos[j].ManualDownloadOnly {
					return nil // already set, no write needed
				}
				s.data.Channels[i].FeedVideos[j].ManualDownloadOnly = true
				return s.save()
			}
		}
		return nil
	}
	return nil
}

// RemoveFeedVideo removes a video from the channel's FeedVideos list.
func (s *Storage) RemoveFeedVideo(channelID, videoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		fvs := s.data.Channels[i].FeedVideos
		for j, fv := range fvs {
			if fv.ID == videoID {
				s.data.Channels[i].FeedVideos = append(fvs[:j], fvs[j+1:]...)
				return s.save()
			}
		}
		return nil
	}
	return nil
}

// PruneFeedVideos removes feed video entries published before the given cutoff time.
func (s *Storage) PruneFeedVideos(channelID string, cutoff time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		var keep []FeedVideo
		for _, fv := range s.data.Channels[i].FeedVideos {
			if !fv.PublishedAt.Before(cutoff) {
				keep = append(keep, fv)
			}
		}
		if len(keep) == len(s.data.Channels[i].FeedVideos) {
			return nil
		}
		s.data.Channels[i].FeedVideos = keep
		return s.save()
	}
	return nil
}

// AddPrunedVideo adds a video directly to a channel's pruned list without
// requiring it to have been downloaded first. Used for shorts rejected at
// download time so they are not re-discovered on the next scheduler run.
func (s *Storage) AddPrunedVideo(channelID, videoID string, publishDate time.Time) error {
	if videoID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		for _, pv := range s.data.Channels[i].PrunedVideos {
			if pv.ID == videoID {
				return nil // already present
			}
		}
		s.data.Channels[i].PrunedVideos = append(s.data.Channels[i].PrunedVideos, PrunedVideo{
			ID:          videoID,
			PublishDate: publishDate,
		})
		return s.save()
	}
	return nil
}

// UpdateChannelID updates the ID field for a channel (used for migrations)
func (s *Storage) UpdateChannelID(oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == oldID {
			s.data.Channels[i].ID = newID
			return s.save()
		}
	}

	return nil
}

// MarkVideoAsDownloaded adds a video ID to the appropriate download list with current timestamp
// Can be used for both channel videos (channelID is a channel ID) or individual videos (channelID is a video ID)
func (s *Storage) MarkVideoAsDownloaded(channelID, videoID, videoTitle string, publishDate time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// First, try to find as a channel
	for i := range s.data.Channels {
		if s.data.Channels[i].ID == channelID {
			// Check if already marked
			for _, vid := range s.data.Channels[i].DownloadedVideos {
				if vid.ID == videoID {
					return nil // Already marked
				}
			}
			s.data.Channels[i].DownloadedVideos = append(s.data.Channels[i].DownloadedVideos, DownloadedVideo{
				ID:           videoID,
				Title:        videoTitle,
				DownloadDate: time.Now().UTC(),
				PublishDate:  NormalizeToUTC(publishDate),
			})
			return s.save()
		}
	}

	// If not found in channels, try to find as an individual video
	for i := range s.data.Videos {
		if s.data.Videos[i].ID == channelID {
			// Check if already marked
			for _, vid := range s.data.Videos[i].DownloadedVideos {
				if vid.ID == videoID {
					return nil // Already marked
				}
			}
			s.data.Videos[i].DownloadedVideos = append(s.data.Videos[i].DownloadedVideos, DownloadedVideo{
				ID:           videoID,
				Title:        videoTitle,
				DownloadDate: time.Now().UTC(),
				PublishDate:  NormalizeToUTC(publishDate),
			})
			return s.save()
		}
	}

	return nil
}

// IsVideoDownloaded checks if a video has been downloaded for a channel or individual video
func (s *Storage) IsVideoDownloaded(channelID, videoID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// First, check in channels
	for _, ch := range s.data.Channels {
		if ch.ID == channelID {
			for _, vid := range ch.DownloadedVideos {
				if vid.ID == videoID {
					return true
				}
			}
			for _, pv := range ch.PrunedVideos {
				if pv.ID == videoID {
					return true
				}
			}
			return false
		}
	}

	// Then, check in individual videos
	for _, v := range s.data.Videos {
		if v.ID == channelID {
			for _, vid := range v.DownloadedVideos {
				if vid.ID == videoID {
					return true
				}
			}
			return false
		}
	}

	return false
}

// MigratePrunedVideos is a one-time migration helper for channels upgraded from
// a schema that did not have the pruned_videos field. It only runs when
// PrunedVideos is nil (never initialized); after this call the slice is non-nil
// so the migration never fires again. Pass the videos to treat as already seen.
func (s *Storage) MigratePrunedVideos(channelID string, videos []PrunedVideo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		if s.data.Channels[i].PrunedVideos != nil {
			return nil // Already initialized, no migration needed.
		}
		seen := map[string]struct{}{}
		result := make([]PrunedVideo, 0, len(videos))
		for _, pv := range videos {
			if pv.ID == "" {
				continue
			}
			if _, dup := seen[pv.ID]; !dup {
				seen[pv.ID] = struct{}{}
				result = append(result, pv)
			}
		}
		s.data.Channels[i].PrunedVideos = result // non-nil even if empty
		return s.save()
	}
	return nil
}

// TrimPrunedVideos removes entries from a channel's pruned list whose publish
// dates predate the given since time. Those videos will never re-appear in feed
// discovery, so there is no reason to keep tracking them.
func (s *Storage) TrimPrunedVideos(channelID string, since time.Time) error {
	if since.IsZero() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}
		before := len(s.data.Channels[i].PrunedVideos)
		if before == 0 {
			return nil
		}
		kept := s.data.Channels[i].PrunedVideos[:0]
		for _, pv := range s.data.Channels[i].PrunedVideos {
			if !pv.PublishDate.IsZero() && pv.PublishDate.Before(since) {
				continue // evict: will never re-appear in discovery
			}
			kept = append(kept, pv)
		}
		if len(kept) == before {
			return nil // nothing changed
		}
		s.data.Channels[i].PrunedVideos = kept
		return s.save()
	}
	return nil
}

// RemoveDownloadedVideo removes a specific video ID from a channel's or video's downloaded list
func (s *Storage) RemoveDownloadedVideo(containerID, videoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// First, try to find as a channel
	for i := range s.data.Channels {
		if s.data.Channels[i].ID == containerID {
			// Remove the video from the downloaded list
			for j := range s.data.Channels[i].DownloadedVideos {
				if s.data.Channels[i].DownloadedVideos[j].ID == videoID {
					pruned := PrunedVideo{
						ID:          s.data.Channels[i].DownloadedVideos[j].ID,
						PublishDate: s.data.Channels[i].DownloadedVideos[j].PublishDate,
					}
					s.data.Channels[i].DownloadedVideos = append(
						s.data.Channels[i].DownloadedVideos[:j],
						s.data.Channels[i].DownloadedVideos[j+1:]...,
					)
					if videoID != "" {
						alreadyTracked := false
						for _, existing := range s.data.Channels[i].PrunedVideos {
							if existing.ID == videoID {
								alreadyTracked = true
								break
							}
						}
						if !alreadyTracked {
							s.data.Channels[i].PrunedVideos = append(s.data.Channels[i].PrunedVideos, pruned)
						}
					}
					return s.save()
				}
			}
			return nil // Video not found in list
		}
	}

	// If not found in channels, try to find as an individual video
	for i := range s.data.Videos {
		if s.data.Videos[i].ID == containerID {
			// Remove the video from the downloaded list
			for j := range s.data.Videos[i].DownloadedVideos {
				if s.data.Videos[i].DownloadedVideos[j].ID == videoID {
					s.data.Videos[i].DownloadedVideos = append(
						s.data.Videos[i].DownloadedVideos[:j],
						s.data.Videos[i].DownloadedVideos[j+1:]...,
					)
					return s.save()
				}
			}
			return nil // Video not found in list
		}
	}

	return nil // Container not found
}

// ReconcileDownloadedVideos removes downloaded_videos entries that no longer
// have a corresponding media file on disk.
func (s *Storage) ReconcileDownloadedVideos(downloadDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	globalFileNames := []string{}
	walkErr := filepath.Walk(downloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		globalFileNames = append(globalFileNames, info.Name())
		return nil
	})

	if walkErr != nil && !os.IsNotExist(walkErr) {
		return walkErr
	}

	changed := false

	for i := range s.data.Channels {
		channelDir := filepath.Join(downloadDir, sanitizeFilename(s.data.Channels[i].Name))
		channelFileNames := []string{}
		entries, err := os.ReadDir(channelDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					channelFileNames = append(channelFileNames, entry.Name())
				}
			}
		}

		filtered := make([]DownloadedVideo, 0, len(s.data.Channels[i].DownloadedVideos))
		for _, tracked := range s.data.Channels[i].DownloadedVideos {
			if hasFileContainingID(channelFileNames, tracked.ID) {
				filtered = append(filtered, tracked)
			} else {
				changed = true
			}
		}
		s.data.Channels[i].DownloadedVideos = filtered
	}

	for i := range s.data.Videos {
		filtered := make([]DownloadedVideo, 0, len(s.data.Videos[i].DownloadedVideos))
		for _, tracked := range s.data.Videos[i].DownloadedVideos {
			if hasFileContainingID(globalFileNames, tracked.ID) {
				filtered = append(filtered, tracked)
			} else {
				changed = true
			}
		}
		s.data.Videos[i].DownloadedVideos = filtered
	}

	if changed {
		return s.save()
	}

	return nil
}

func hasFileContainingID(fileNames []string, videoID string) bool {
	if videoID == "" {
		return false
	}

	for _, name := range fileNames {
		if strings.Contains(name, videoID) {
			return true
		}
	}

	return false
}

// UpdateChannelDownloadedVideoPruning updates per-downloaded-video pruning behavior for a channel entry.
func (s *Storage) UpdateChannelDownloadedVideoPruning(channelID, videoID string, disablePruning bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID != channelID {
			continue
		}

		for j := range s.data.Channels[i].DownloadedVideos {
			if s.data.Channels[i].DownloadedVideos[j].ID == videoID {
				s.data.Channels[i].DownloadedVideos[j].DisablePruning = disablePruning
				return s.save()
			}
		}

		return fmt.Errorf("downloaded video %s not found in channel %s", videoID, channelID)
	}

	return fmt.Errorf("channel %s not found", channelID)
}

// GetVideos returns all videos
func (s *Storage) GetVideos() []Video {
	s.mu.RLock()
	defer s.mu.RUnlock()

	videos := make([]Video, len(s.data.Videos))
	copy(videos, s.data.Videos)
	return videos
}

// GetVideo returns the video with the given ID, if it exists.
func (s *Storage) GetVideo(id string) (Video, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.data.Videos {
		if v.ID == id {
			return v, true
		}
	}
	return Video{}, false
}

// HasVideo returns true if a video entry with the given ID exists
func (s *Storage) HasVideo(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.data.Videos {
		if v.ID == id {
			return true
		}
	}

	return false
}

// AddVideo adds a new video
func (s *Storage) AddVideo(video Video) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if video.AddedDate.IsZero() {
		video.AddedDate = time.Now().UTC()
	}

	s.data.Videos = append(s.data.Videos, video)
	if err := s.save(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// RemoveVideo removes a video by ID
func (s *Storage) RemoveVideo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, vid := range s.data.Videos {
		if vid.ID == id {
			s.data.Videos = append(s.data.Videos[:i], s.data.Videos[i+1:]...)
			if err := s.save(); err != nil {
				return err
			}
			s.notify()
			return nil
		}
	}

	return nil
}

// UpdateVideoLastChecked updates the last checked time for a video
func (s *Storage) UpdateVideoLastChecked(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Videos {
		if s.data.Videos[i].ID == id {
			s.data.Videos[i].LastChecked = t
			return s.save()
		}
	}

	return nil
}

// UpdateVideo updates retention, pruning behavior, quality, format, and shorts preference for a video.
func (s *Storage) UpdateVideo(id string, retentionDays int, disablePruning bool, videoQuality, videoFormat string, downloadShorts bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Videos {
		if s.data.Videos[i].ID == id {
			s.data.Videos[i].RetentionDays = retentionDays
			s.data.Videos[i].DisablePruning = disablePruning
			s.data.Videos[i].VideoQuality = videoQuality
			s.data.Videos[i].VideoFormat = videoFormat
			s.data.Videos[i].DownloadShorts = downloadShorts
			return s.save()
		}
	}

	return nil
}

// UpdateVideoUploaderInfo caches uploader metadata for a video to avoid re-querying yt-dlp
func (s *Storage) UpdateVideoUploaderInfo(id, uploader, uploaderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Videos {
		if s.data.Videos[i].ID == id {
			s.data.Videos[i].Uploader = strings.TrimSpace(uploader)
			s.data.Videos[i].UploaderID = strings.TrimSpace(uploaderID)
			return s.save()
		}
	}

	return nil
}

// SetChannelError sets the error message for a channel
func (s *Storage) SetChannelError(id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].LastError = errMsg
			s.data.Channels[i].LastErrorTime = time.Now()
			return s.save()
		}
	}

	return nil
}

// ClearChannelError clears the error message for a channel
func (s *Storage) ClearChannelError(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].LastError = ""
			s.data.Channels[i].LastErrorTime = time.Time{}
			return s.save()
		}
	}

	return nil
}

// SetVideoError sets the error message for a video
func (s *Storage) SetVideoError(id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Videos {
		if s.data.Videos[i].ID == id {
			s.data.Videos[i].LastError = errMsg
			s.data.Videos[i].LastErrorTime = time.Now()
			return s.save()
		}
	}

	return nil
}

// ClearVideoError clears the error message for a video
func (s *Storage) ClearVideoError(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Videos {
		if s.data.Videos[i].ID == id {
			s.data.Videos[i].LastError = ""
			s.data.Videos[i].LastErrorTime = time.Time{}
			return s.save()
		}
	}

	return nil
}

// MergeChannelDownloadedVideos appends downloaded video entries into a channel's list,
// skipping any video IDs already present.
func (s *Storage) MergeChannelDownloadedVideos(id string, videos []DownloadedVideo) error {
	if len(videos) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID != id {
			continue
		}
		existing := make(map[string]struct{}, len(s.data.Channels[i].DownloadedVideos))
		for _, dv := range s.data.Channels[i].DownloadedVideos {
			existing[dv.ID] = struct{}{}
		}
		for _, dv := range videos {
			if _, seen := existing[dv.ID]; !seen {
				s.data.Channels[i].DownloadedVideos = append(s.data.Channels[i].DownloadedVideos, dv)
				existing[dv.ID] = struct{}{}
			}
		}
		return s.save()
	}
	return fmt.Errorf("channel %s not found", id)
}

// SetChannelThumbnailIfEmpty sets the channel thumbnail URL only if it is not already populated.
func (s *Storage) SetChannelThumbnailIfEmpty(id, url string) error {
	if url == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			if s.data.Channels[i].ThumbnailURL == "" {
				s.data.Channels[i].ThumbnailURL = url
				return s.save()
			}
			return nil
		}
	}
	return nil
}

// MigrateChannelIDs resolves and updates channel IDs for any channels that don't have proper UC... IDs
// This should be called during startup to ensure all channels have canonical channel IDs
func (s *Storage) MigrateChannelIDs(downloader *Downloader) (migratedCount int, errors []string) {
	s.mu.RLock()
	channelsToMigrate := []int{}
	for i, ch := range s.data.Channels {
		// Check if the channel ID is not a proper UC... format (canonical YouTube ID)
		if !strings.HasPrefix(ch.ID, "UC") {
			channelsToMigrate = append(channelsToMigrate, i)
		}
	}
	s.mu.RUnlock()

	if len(channelsToMigrate) == 0 {
		return 0, nil
	}

	log.Printf("Found %d channel(s) needing ID migration", len(channelsToMigrate))

	// Process each channel that needs migration
	for _, idx := range channelsToMigrate {
		s.mu.RLock()
		channel := s.data.Channels[idx]
		s.mu.RUnlock()

		// Resolve the canonical channel ID using yt-dlp
		newID, err := downloader.ResolveChannelID(channel.URL)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to resolve channel ID for %s (%s): %v", channel.Name, channel.URL, err)
			log.Printf("Migration error: %s", errMsg)
			errors = append(errors, errMsg)
			continue
		}

		// Update the channel ID in storage
		if err := s.UpdateChannelID(channel.ID, newID); err != nil {
			errMsg := fmt.Sprintf("Failed to update channel ID for %s: %v", channel.Name, err)
			log.Printf("Migration error: %s", errMsg)
			errors = append(errors, errMsg)
			continue
		}

		log.Printf("Successfully migrated channel %s: %s → %s", channel.Name, channel.ID, newID)
		migratedCount++
	}

	return migratedCount, errors
}
