package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVideoInfoJSONParsing(t *testing.T) {
	tests := []struct {
		name     string
		jsonData string
		wantErr  bool
		checkFn  func(*testing.T, *VideoInfo)
	}{
		{
			name: "valid complete video info",
			jsonData: `{
				"id": "dQw4w9WgXcQ",
				"title": "Test Video",
				"upload_date": "20240115",
				"uploader": "Test Channel",
				"uploader_id": "testchannel123"
			}`,
			wantErr: false,
			checkFn: func(t *testing.T, info *VideoInfo) {
				if info.ID != "dQw4w9WgXcQ" {
					t.Errorf("Expected ID 'dQw4w9WgXcQ', got '%s'", info.ID)
				}
				if info.Title != "Test Video" {
					t.Errorf("Expected title 'Test Video', got '%s'", info.Title)
				}
				if info.UploadDate != "20240115" {
					t.Errorf("Expected upload date '20240115', got '%s'", info.UploadDate)
				}
				if info.Uploader != "Test Channel" {
					t.Errorf("Expected uploader 'Test Channel', got '%s'", info.Uploader)
				}
			},
		},
		{
			name: "minimal video info",
			jsonData: `{
				"id": "abc123",
				"title": "Minimal Video"
			}`,
			wantErr: false,
			checkFn: func(t *testing.T, info *VideoInfo) {
				if info.ID != "abc123" {
					t.Errorf("Expected ID 'abc123', got '%s'", info.ID)
				}
				if info.UploadDate != "" {
					t.Errorf("Expected empty upload date, got '%s'", info.UploadDate)
				}
			},
		},
		{
			name:     "invalid JSON",
			jsonData: `{"id": "test", "title":`,
			wantErr:  true,
		},
		{
			name:     "empty JSON",
			jsonData: `{}`,
			wantErr:  false,
			checkFn: func(t *testing.T, info *VideoInfo) {
				if info.ID != "" {
					t.Errorf("Expected empty ID, got '%s'", info.ID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var info VideoInfo
			err := json.Unmarshal([]byte(tt.jsonData), &info)

			if (err != nil) != tt.wantErr {
				t.Errorf("json.Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.checkFn != nil {
				tt.checkFn(t, &info)
			}
		})
	}
}

func TestUploadDateParsing(t *testing.T) {
	tests := []struct {
		name       string
		uploadDate string
		wantTime   time.Time
		wantErr    bool
	}{
		{
			name:       "valid date",
			uploadDate: "20240115",
			wantTime:   time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			wantErr:    false,
		},
		{
			name:       "another valid date",
			uploadDate: "20231225",
			wantTime:   time.Date(2023, 12, 25, 0, 0, 0, 0, time.UTC),
			wantErr:    false,
		},
		{
			name:       "leap year date",
			uploadDate: "20240229",
			wantTime:   time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			wantErr:    false,
		},
		{
			name:       "invalid date format",
			uploadDate: "2024-01-15",
			wantErr:    true,
		},
		{
			name:       "invalid month",
			uploadDate: "20241315",
			wantErr:    true,
		},
		{
			name:       "invalid day",
			uploadDate: "20240132",
			wantErr:    true,
		},
		{
			name:       "empty date",
			uploadDate: "",
			wantErr:    true,
		},
		{
			name:       "malformed short date",
			uploadDate: "2024",
			wantErr:    true,
		},
		{
			name:       "malformed long date",
			uploadDate: "202401151200",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This is the same parsing logic used in downloader.go
			parsedTime, err := time.Parse("20060102", tt.uploadDate)

			if (err != nil) != tt.wantErr {
				t.Errorf("time.Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if !parsedTime.Equal(tt.wantTime) {
					t.Errorf("time.Parse() = %v, want %v", parsedTime, tt.wantTime)
				}
			}
		})
	}
}

func TestVideoInfoWithParsedDate(t *testing.T) {
	tests := []struct {
		name        string
		uploadDate  string
		expectValid bool
		expectedDay int
	}{
		{
			name:        "valid date gets parsed",
			uploadDate:  "20240315",
			expectValid: true,
			expectedDay: 15,
		},
		{
			name:        "invalid date remains zero",
			uploadDate:  "invalid",
			expectValid: false,
		},
		{
			name:        "empty date remains zero",
			uploadDate:  "",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the parsing done in GetVideoInfo/GetChannelVideos
			info := VideoInfo{
				ID:         "test123",
				UploadDate: tt.uploadDate,
			}

			// Parse upload date (same logic as in downloader.go)
			if info.UploadDate != "" {
				t, err := time.Parse("20060102", info.UploadDate)
				if err == nil {
					info.PublishTime = t
				}
			}

			if tt.expectValid {
				if info.PublishTime.IsZero() {
					t.Error("Expected PublishTime to be set, but it's zero")
				}
				if info.PublishTime.Day() != tt.expectedDay {
					t.Errorf("Expected day %d, got %d", tt.expectedDay, info.PublishTime.Day())
				}
			} else {
				if !info.PublishTime.IsZero() {
					t.Errorf("Expected PublishTime to be zero, got %v", info.PublishTime)
				}
			}
		})
	}
}

func TestVideoFilteringBySinceDate(t *testing.T) {
	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	weekAgo := now.AddDate(0, 0, -7)
	monthAgo := now.AddDate(0, -1, 0)

	videos := []VideoInfo{
		{ID: "1", UploadDate: now.Format("20060102"), PublishTime: now},
		{ID: "2", UploadDate: yesterday.Format("20060102"), PublishTime: yesterday},
		{ID: "3", UploadDate: weekAgo.Format("20060102"), PublishTime: weekAgo},
		{ID: "4", UploadDate: monthAgo.Format("20060102"), PublishTime: monthAgo},
	}

	tests := []struct {
		name          string
		since         time.Time
		expectedCount int
		expectedIDs   []string
	}{
		{
			name:          "since week ago includes recent videos",
			since:         weekAgo.Add(-time.Second), // Slightly before to be inclusive
			expectedCount: 3,
			expectedIDs:   []string{"1", "2", "3"},
		},
		{
			name:          "since yesterday includes recent videos",
			since:         yesterday.Add(-time.Second), // Slightly before to be inclusive
			expectedCount: 2,
			expectedIDs:   []string{"1", "2"},
		},
		{
			name:          "since month ago includes all videos",
			since:         monthAgo.Add(-time.Second), // Slightly before to be inclusive
			expectedCount: 4,
			expectedIDs:   []string{"1", "2", "3", "4"},
		},
		{
			name:          "since future includes no videos",
			since:         now.AddDate(0, 0, 1),
			expectedCount: 0,
			expectedIDs:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate filtering logic from GetChannelVideos
			var filtered []VideoInfo
			for _, v := range videos {
				if v.PublishTime.After(tt.since) {
					filtered = append(filtered, v)
				}
			}

			if len(filtered) != tt.expectedCount {
				t.Errorf("Expected %d videos, got %d", tt.expectedCount, len(filtered))
			}

			for i, expectedID := range tt.expectedIDs {
				if i >= len(filtered) {
					t.Errorf("Missing expected video ID '%s'", expectedID)
					continue
				}
				if filtered[i].ID != expectedID {
					t.Errorf("Expected video ID '%s' at position %d, got '%s'", expectedID, i, filtered[i].ID)
				}
			}
		})
	}
}

func TestDateBoundaryConditions(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		wantYear int
		wantDay  int
	}{
		{
			name:     "first day of year",
			date:     "20240101",
			wantYear: 2024,
			wantDay:  1,
		},
		{
			name:     "last day of year",
			date:     "20241231",
			wantYear: 2024,
			wantDay:  31,
		},
		{
			name:     "end of february non-leap",
			date:     "20230228",
			wantYear: 2023,
			wantDay:  28,
		},
		{
			name:     "end of february leap year",
			date:     "20240229",
			wantYear: 2024,
			wantDay:  29,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := time.Parse("20060102", tt.date)
			if err != nil {
				t.Fatalf("Failed to parse date: %v", err)
			}

			if parsed.Year() != tt.wantYear {
				t.Errorf("Expected year %d, got %d", tt.wantYear, parsed.Year())
			}

			if parsed.Day() != tt.wantDay {
				t.Errorf("Expected day %d, got %d", tt.wantDay, parsed.Day())
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no special characters",
			input:    "normal_filename",
			expected: "normal_filename",
		},
		{
			name:     "with forward slash",
			input:    "path/to/file",
			expected: "path_to_file",
		},
		{
			name:     "with backslash",
			input:    "path\\to\\file",
			expected: "path_to_file",
		},
		{
			name:     "with colon",
			input:    "C:\\Users\\test",
			expected: "C__Users_test",
		},
		{
			name:     "with multiple invalid chars",
			input:    "video:title?with*special<chars>",
			expected: "video_title_with_special_chars_",
		},
		{
			name:     "with quotes and pipes",
			input:    "test\"file\"|name",
			expected: "test_file__name",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "unnamed",
		},
		{
			name:     "only spaces",
			input:    "   ",
			expected: "unnamed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEscapeXML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no special characters",
			input:    "normal text",
			expected: "normal text",
		},
		{
			name:     "with ampersand",
			input:    "Tom & Jerry",
			expected: "Tom &amp; Jerry",
		},
		{
			name:     "with less than",
			input:    "5 < 10",
			expected: "5 &lt; 10",
		},
		{
			name:     "with greater than",
			input:    "10 > 5",
			expected: "10 &gt; 5",
		},
		{
			name:     "with quotes",
			input:    "He said \"hello\"",
			expected: "He said &quot;hello&quot;",
		},
		{
			name:     "with apostrophes",
			input:    "It's a test",
			expected: "It&apos;s a test",
		},
		{
			name:     "with multiple special chars",
			input:    "<tag attr=\"value\" & 'test'>",
			expected: "&lt;tag attr=&quot;value&quot; &amp; &apos;test&apos;&gt;",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := escapeXML(tt.input)
			if result != tt.expected {
				t.Errorf("escapeXML(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestAddJitterSeconds(t *testing.T) {
	tests := []struct {
		name          string
		baseSeconds   int
		jitterPercent float64
		minExpected   int
		maxExpected   int
	}{
		{
			name:          "zero base",
			baseSeconds:   0,
			jitterPercent: 0.5,
			minExpected:   0,
			maxExpected:   0,
		},
		{
			name:          "negative base",
			baseSeconds:   -10,
			jitterPercent: 0.5,
			minExpected:   0,
			maxExpected:   0,
		},
		{
			name:          "10 seconds with 50% jitter",
			baseSeconds:   10,
			jitterPercent: 0.5,
			minExpected:   10,
			maxExpected:   15,
		},
		{
			name:          "100 seconds with 30% jitter",
			baseSeconds:   100,
			jitterPercent: 0.3,
			minExpected:   100,
			maxExpected:   130,
		},
		{
			name:          "zero jitter percent",
			baseSeconds:   50,
			jitterPercent: 0.0,
			minExpected:   50,
			maxExpected:   50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to test randomness
			for i := 0; i < 20; i++ {
				result := addJitterSeconds(tt.baseSeconds, tt.jitterPercent)
				if result < tt.minExpected || result > tt.maxExpected {
					t.Errorf("addJitterSeconds(%d, %f) = %d, want range [%d, %d]",
						tt.baseSeconds, tt.jitterPercent, result, tt.minExpected, tt.maxExpected)
				}
			}
		})
	}
}

func TestBuildFormatString(t *testing.T) {
	config := DefaultConfig()
	downloader := NewDownloader(config)

	tests := []struct {
		name           string
		quality        string
		format         string
		expectedSubstr []string // Substrings that should appear in result
	}{
		{
			name:           "default quality and mp4",
			quality:        "",
			format:         "mp4",
			expectedSubstr: []string{"bestvideo[ext=mp4]", "bestvideo+bestaudio/best"},
		},
		{
			name:           "best quality with webm",
			quality:        "best",
			format:         "webm",
			expectedSubstr: []string{"bestvideo[ext=webm]", "bestvideo+bestaudio/best"},
		},
		{
			name:           "720p with mp4",
			quality:        "720",
			format:         "mp4",
			expectedSubstr: []string{"bestvideo[height<=720][ext=mp4]", "+bestaudio/"},
		},
		{
			name:           "480p with mkv",
			quality:        "480",
			format:         "mkv",
			expectedSubstr: []string{"bestvideo[height<=480][ext=mkv]", "+bestaudio/"},
		},
		{
			name:           "empty format defaults to mp4",
			quality:        "1080",
			format:         "",
			expectedSubstr: []string{"bestvideo[height<=1080][ext=mp4]"},
		},
		{
			name:           "360p quality",
			quality:        "360",
			format:         "webm",
			expectedSubstr: []string{"bestvideo[height<=360][ext=webm]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := downloader.buildFormatString(tt.quality, tt.format)
			for _, substr := range tt.expectedSubstr {
				if !strings.Contains(result, substr) {
					t.Errorf("buildFormatString(%q, %q) = %q, expected to contain %q",
						tt.quality, tt.format, result, substr)
				}
			}
		})
	}
}

func TestIsSkippableYtDlpOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "empty string",
			output:   "",
			expected: false,
		},
		{
			name:     "filtered out message",
			output:   "[youtube] ABC123: Video does not pass filter",
			expected: true,
		},
		{
			name:     "video unavailable",
			output:   "ERROR: [youtube] ABC123: Video unavailable",
			expected: true,
		},
		{
			name:     "private video",
			output:   "ERROR: [youtube] ABC123: Private video",
			expected: true,
		},
		{
			name:     "members only",
			output:   "ERROR: [youtube] ABC123: Members-only content",
			expected: true,
		},
		{
			name:     "format not available",
			output:   "ERROR: Requested format is not available",
			expected: true,
		},
		{
			name:     "no video formats found",
			output:   "ERROR: No video formats found",
			expected: true,
		},
		{
			name:     "normal error",
			output:   "ERROR: Network timeout",
			expected: false,
		},
		{
			name:     "success message",
			output:   "Downloaded successfully",
			expected: false,
		},
		{
			name:     "case insensitive",
			output:   "VIDEO UNAVAILABLE",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSkippableYtDlpOutput(tt.output)
			if result != tt.expected {
				t.Errorf("isSkippableYtDlpOutput(%q) = %v, want %v", tt.output, result, tt.expected)
			}
		})
	}
}

func TestExtractSkipReason(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "empty string",
			output:   "",
			expected: "filtered out or unavailable",
		},
		{
			name:     "filtered message",
			output:   "[youtube] ABC123: Video does not pass filter",
			expected: "[youtube] ABC123: Video does not pass filter",
		},
		{
			name:     "unavailable message",
			output:   "ERROR: [youtube] ABC123: Video unavailable",
			expected: "ERROR: [youtube] ABC123: Video unavailable",
		},
		{
			name:     "private video",
			output:   "ERROR: Private video. Sign in if you've been granted access",
			expected: "ERROR: Private video. Sign in if you've been granted access",
		},
		{
			name:     "multiline with filter at end",
			output:   "Some info\nProcessing\n[youtube] ABC: Video does not pass filter",
			expected: "[youtube] ABC: Video does not pass filter",
		},
		{
			name:     "whitespace only",
			output:   "   \n  \n  ",
			expected: "filtered out or unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractSkipReason(tt.output)
			if result != tt.expected {
				t.Errorf("extractSkipReason(%q) = %q, want %q", tt.output, result, tt.expected)
			}
		})
	}
}

func TestDownloadResult(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		result := &DownloadResult{}
		if result.Downloaded {
			t.Error("Expected Downloaded to be false by default")
		}
		if result.Skipped {
			t.Error("Expected Skipped to be false by default")
		}
		if result.SkipReason != "" {
			t.Error("Expected SkipReason to be empty by default")
		}
	})

	t.Run("set as downloaded", func(t *testing.T) {
		result := &DownloadResult{Downloaded: true}
		if !result.Downloaded {
			t.Error("Expected Downloaded to be true")
		}
		if result.Skipped {
			t.Error("Expected Skipped to be false")
		}
	})

	t.Run("set as skipped with reason", func(t *testing.T) {
		result := &DownloadResult{
			Skipped:    true,
			SkipReason: "Video unavailable",
		}
		if result.Downloaded {
			t.Error("Expected Downloaded to be false")
		}
		if !result.Skipped {
			t.Error("Expected Skipped to be true")
		}
		if result.SkipReason != "Video unavailable" {
			t.Errorf("Expected SkipReason 'Video unavailable', got %q", result.SkipReason)
		}
	})
}

func TestCountVideoFiles(t *testing.T) {
	// Create temp directory for testing
	tmpDir := t.TempDir()
	config := DefaultConfig()
	downloader := NewDownloader(config)

	videoID := "test-video-123"

	t.Run("empty directory", func(t *testing.T) {
		count := downloader.countVideoFiles(tmpDir, videoID)
		if count != 0 {
			t.Errorf("Expected 0 files, got %d", count)
		}
	})

	t.Run("with matching video files", func(t *testing.T) {
		// Create test files
		os.WriteFile(filepath.Join(tmpDir, "Channel Name - test-video-123.mp4"), []byte("test"), 0644)
		os.WriteFile(filepath.Join(tmpDir, "test-video-123.webm"), []byte("test"), 0644)

		count := downloader.countVideoFiles(tmpDir, videoID)
		if count != 2 {
			t.Errorf("Expected 2 files, got %d", count)
		}
	})

	t.Run("excludes nfo files", func(t *testing.T) {
		// Add NFO file
		os.WriteFile(filepath.Join(tmpDir, "test-video-123.nfo"), []byte("metadata"), 0644)

		count := downloader.countVideoFiles(tmpDir, videoID)
		if count != 2 {
			t.Errorf("Expected 2 files (NFO excluded), got %d", count)
		}
	})

	t.Run("ignores non-matching files", func(t *testing.T) {
		// Add non-matching files
		os.WriteFile(filepath.Join(tmpDir, "other-video-456.mp4"), []byte("test"), 0644)
		os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("test"), 0644)

		count := downloader.countVideoFiles(tmpDir, videoID)
		if count != 2 {
			t.Errorf("Expected 2 files (non-matching excluded), got %d", count)
		}
	})

	t.Run("empty video ID returns 0", func(t *testing.T) {
		count := downloader.countVideoFiles(tmpDir, "")
		if count != 0 {
			t.Errorf("Expected 0 files for empty video ID, got %d", count)
		}
	})

	t.Run("non-existent directory returns 0", func(t *testing.T) {
		count := downloader.countVideoFiles("/non/existent/path", videoID)
		if count != 0 {
			t.Errorf("Expected 0 files for non-existent directory, got %d", count)
		}
	})
}

func TestCleanOldVideosForChannelOnlyRemovesTrackedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	storagePath := filepath.Join(tmpDir, "storage.json")
	storage, err := NewStorage(storagePath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channelName := "Tracked Channel"
	channelDir := filepath.Join(tmpDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	trackedOld := filepath.Join(channelDir, "tracked-old-abc123.mp4")
	untrackedOld := filepath.Join(channelDir, "someone-else-file.mp4")
	trackedRecent := filepath.Join(channelDir, "tracked-recent-def456.mp4")

	for _, file := range []string{trackedOld, untrackedOld, trackedRecent} {
		if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", file, err)
		}
	}

	now := time.Now()
	oldTime := now.AddDate(0, 0, -10)
	recentTime := now.AddDate(0, 0, -2)
	if err := os.Chtimes(trackedOld, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes(trackedOld) error = %v", err)
	}
	if err := os.Chtimes(untrackedOld, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes(untrackedOld) error = %v", err)
	}
	if err := os.Chtimes(trackedRecent, recentTime, recentTime); err != nil {
		t.Fatalf("Chtimes(trackedRecent) error = %v", err)
	}

	err = storage.AddChannel(Channel{
		ID:   "channel-1",
		Name: channelName,
		DownloadedVideos: []DownloadedVideo{
			{ID: "abc123", Title: "Old tracked", DownloadDate: oldTime},
			{ID: "def456", Title: "Recent tracked", DownloadDate: recentTime},
		},
	})
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	if err := downloader.CleanOldVideosForChannel(channelName, "channel-1", 7, storage); err != nil {
		t.Fatalf("CleanOldVideosForChannel() error = %v", err)
	}

	if _, err := os.Stat(trackedOld); !os.IsNotExist(err) {
		t.Fatalf("expected tracked old file to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(untrackedOld); err != nil {
		t.Fatalf("expected untracked file to remain, stat err = %v", err)
	}
	if _, err := os.Stat(trackedRecent); err != nil {
		t.Fatalf("expected recent tracked file to remain, stat err = %v", err)
	}
}

func TestCleanOldVideosForVideoOnlyRemovesTrackedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	storagePath := filepath.Join(tmpDir, "storage.json")
	storage, err := NewStorage(storagePath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channelDir := filepath.Join(tmpDir, "Uploader")
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	trackedOld := filepath.Join(channelDir, "tracked-old-xyz123.mp4")
	untrackedOld := filepath.Join(channelDir, "manual-file.mp4")
	trackedRecent := filepath.Join(channelDir, "tracked-recent-uvw999.mp4")

	for _, file := range []string{trackedOld, untrackedOld, trackedRecent} {
		if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", file, err)
		}
	}

	now := time.Now()
	oldTime := now.AddDate(0, 0, -10)
	recentTime := now.AddDate(0, 0, -1)
	if err := os.Chtimes(trackedOld, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes(trackedOld) error = %v", err)
	}
	if err := os.Chtimes(untrackedOld, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes(untrackedOld) error = %v", err)
	}
	if err := os.Chtimes(trackedRecent, recentTime, recentTime); err != nil {
		t.Fatalf("Chtimes(trackedRecent) error = %v", err)
	}

	err = storage.AddVideo(Video{
		ID:    "video-entry-1",
		Title: "Tracked video entry",
		DownloadedVideos: []DownloadedVideo{
			{ID: "xyz123", Title: "Old tracked", DownloadDate: oldTime},
			{ID: "uvw999", Title: "Recent tracked", DownloadDate: recentTime},
		},
	})
	if err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	if err := downloader.CleanOldVideosForVideo("Tracked video entry", "video-entry-1", 7, storage); err != nil {
		t.Fatalf("CleanOldVideosForVideo() error = %v", err)
	}

	if _, err := os.Stat(trackedOld); !os.IsNotExist(err) {
		t.Fatalf("expected tracked old file to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(untrackedOld); err != nil {
		t.Fatalf("expected untracked file to remain, stat err = %v", err)
	}
	if _, err := os.Stat(trackedRecent); err != nil {
		t.Fatalf("expected recent tracked file to remain, stat err = %v", err)
	}
}
