package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// DownloadedVideo tracks a downloaded video with its download date
type DownloadedVideo struct {
	ID           string    `json:"id"`
	DownloadDate time.Time `json:"download_date"`
}

// Channel represents a YouTube channel to monitor
type Channel struct {
	ID               string            `json:"id"`
	URL              string            `json:"url"`
	Name             string            `json:"name"`
	LastChecked      time.Time         `json:"last_checked"`
	RetentionDays    int               `json:"retention_days"`
	CutoffDate       time.Time         `json:"cutoff_date"`          // Don't download videos published before this date
	VideoQuality     string            `json:"video_quality"`        // Video quality preference (e.g., "best", "720", "480", "360")
	DownloadShorts   bool              `json:"download_shorts"`      // Whether to download short-format videos
	DownloadedVideos []DownloadedVideo `json:"downloaded_videos"`    // Track which videos have been downloaded with dates
	LastError        string            `json:"last_error,omitempty"` // Most recent error message
	LastErrorTime    time.Time         `json:"last_error_time,omitempty"`
}

// Video represents a specific YouTube video to monitor
type Video struct {
	ID            string    `json:"id"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	LastChecked   time.Time `json:"last_checked"`
	RetentionDays int       `json:"retention_days"`
	LastError     string    `json:"last_error,omitempty"` // Most recent error message
	LastErrorTime time.Time `json:"last_error_time,omitempty"`
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

// UpdateChannel updates retention days, cutoff date, video quality, and shorts preference for a channel
func (s *Storage) UpdateChannel(id string, retentionDays int, cutoffDate time.Time, videoQuality string, downloadShorts bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].RetentionDays = retentionDays
			s.data.Channels[i].CutoffDate = cutoffDate
			s.data.Channels[i].VideoQuality = videoQuality
			s.data.Channels[i].DownloadShorts = downloadShorts
			return s.save()
		}
	}

	return nil
}

// MarkVideoAsDownloaded adds a video ID to the channel's downloaded list with current timestamp
func (s *Storage) MarkVideoAsDownloaded(channelID, videoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
				DownloadDate: time.Now(),
			})
			return s.save()
		}
	}

	return nil
}

// IsVideoDownloaded checks if a video has been downloaded for a channel
func (s *Storage) IsVideoDownloaded(channelID, videoID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

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

	return false
}

// GetVideoDownloadDate returns the download date for a video, or zero time if not found
func (s *Storage) GetVideoDownloadDate(channelID, videoID string) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ch := range s.data.Channels {
		if ch.ID == channelID {
			for _, vid := range ch.DownloadedVideos {
				if vid.ID == videoID {
					return vid.DownloadDate
				}
			}
			break
		}
	}

	return time.Time{}
}

// GetVideos returns all videos
func (s *Storage) GetVideos() []Video {
	s.mu.RLock()
	defer s.mu.RUnlock()

	videos := make([]Video, len(s.data.Videos))
	copy(videos, s.data.Videos)
	return videos
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
