package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// DownloadedVideo tracks a downloaded video with its download date
type DownloadedVideo struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	DownloadDate   time.Time `json:"download_date"`
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
	CutoffDate       time.Time         `json:"cutoff_date"`          // Don't download videos published before this date
	VideoQuality     string            `json:"video_quality"`        // Video quality preference (e.g., "best", "720", "480", "360")
	VideoFormat      string            `json:"video_format"`         // Video format preference (e.g., "mp4", "webm", "mkv")
	DownloadShorts   bool              `json:"download_shorts"`      // Whether to download short-format videos
	DownloadedVideos []DownloadedVideo `json:"downloaded_videos"`    // Track which videos have been downloaded with dates
	LastError        string            `json:"last_error,omitempty"` // Most recent error message
	LastErrorTime    time.Time         `json:"last_error_time,omitempty"`
}

// Video represents a specific YouTube video to monitor
type Video struct {
	ID               string            `json:"id"`
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	LastChecked      time.Time         `json:"last_checked"`
	RetentionDays    int               `json:"retention_days"`
	DisablePruning   bool              `json:"disable_pruning"`
	VideoQuality     string            `json:"video_quality"`        // Video quality preference (e.g., "best", "720", "480", "360")
	VideoFormat      string            `json:"video_format"`         // Video format preference (e.g., "mp4", "webm", "mkv")
	DownloadShorts   bool              `json:"download_shorts"`      // Whether to download short-format videos
	DownloadedVideos []DownloadedVideo `json:"downloaded_videos"`    // Track which videos have been downloaded with dates
	LastError        string            `json:"last_error,omitempty"` // Most recent error message
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
}

// NewStorage creates a new Storage instance
func NewStorage(filePath string) (*Storage, error) {
	s := &Storage{
		filePath: filePath,
		data:     StorageData{Channels: []Channel{}, Videos: []Video{}},
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

// GetChannels returns all channels
func (s *Storage) GetChannels() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()

	channels := make([]Channel, len(s.data.Channels))
	copy(channels, s.data.Channels)
	return channels
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
	return s.save()
}

// RemoveChannel removes a channel by ID
func (s *Storage) RemoveChannel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, ch := range s.data.Channels {
		if ch.ID == id {
			s.data.Channels = append(s.data.Channels[:i], s.data.Channels[i+1:]...)
			return s.save()
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

// UpdateChannel updates retention days, pruning behavior, cutoff date, video quality, video format, and shorts preference for a channel
func (s *Storage) UpdateChannel(id string, retentionDays int, disablePruning bool, cutoffDate time.Time, videoQuality, videoFormat string, downloadShorts bool) error {
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
			return s.save()
		}
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
func (s *Storage) MarkVideoAsDownloaded(channelID, videoID, videoTitle string) error {
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
				DownloadDate: time.Now(),
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
				DownloadDate: time.Now(),
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
					s.data.Channels[i].DownloadedVideos = append(
						s.data.Channels[i].DownloadedVideos[:j],
						s.data.Channels[i].DownloadedVideos[j+1:]...,
					)
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

	s.data.Videos = append(s.data.Videos, video)
	return s.save()
}

// RemoveVideo removes a video by ID
func (s *Storage) RemoveVideo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, vid := range s.data.Videos {
		if vid.ID == id {
			s.data.Videos = append(s.data.Videos[:i], s.data.Videos[i+1:]...)
			return s.save()
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
