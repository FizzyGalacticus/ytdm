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

func TestResolveChannelID(t *testing.T) {
	t.Run("uses channel_id from yt-dlp output", func(t *testing.T) {
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "fake-yt-dlp.sh")
		scriptContent := "#!/bin/sh\necho '{\"channel_id\":\"UC123abc\",\"uploader_id\":\"@handle\"}'\n"
		if err := os.WriteFile(script, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("failed to write fake yt-dlp script: %v", err)
		}

		cfg := DefaultConfig()
		cfg.YtDlp.Path = script
		downloader := NewDownloader(cfg)

		id, err := downloader.ResolveChannelID("https://www.youtube.com/@somehandle")
		if err != nil {
			t.Fatalf("ResolveChannelID() error = %v", err)
		}
		if id != "UC123abc" {
			t.Fatalf("ResolveChannelID() = %q, want %q", id, "UC123abc")
		}
	})

	t.Run("falls back to /channel/ URL extraction", func(t *testing.T) {
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "fake-yt-dlp.sh")
		scriptContent := "#!/bin/sh\necho '{\"uploader_id\":\"@handle\"}'\n"
		if err := os.WriteFile(script, []byte(scriptContent), 0755); err != nil {
			t.Fatalf("failed to write fake yt-dlp script: %v", err)
		}

		cfg := DefaultConfig()
		cfg.YtDlp.Path = script
		downloader := NewDownloader(cfg)

		id, err := downloader.ResolveChannelID("https://www.youtube.com/channel/UCfallback123")
		if err != nil {
			t.Fatalf("ResolveChannelID() error = %v", err)
		}
		if id != "UCfallback123" {
			t.Fatalf("ResolveChannelID() = %q, want %q", id, "UCfallback123")
		}
	})
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

	t.Run("excludes sidecar metadata and thumbnail files", func(t *testing.T) {
		os.WriteFile(filepath.Join(tmpDir, "test-video-123.info.json"), []byte("{}"), 0644)
		os.WriteFile(filepath.Join(tmpDir, "test-video-123.jpg"), []byte("jpg"), 0644)
		os.WriteFile(filepath.Join(tmpDir, "test-video-123.webp"), []byte("webp"), 0644)

		count := downloader.countVideoFiles(tmpDir, videoID)
		if count != 2 {
			t.Errorf("Expected 2 files (sidecars excluded), got %d", count)
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

func TestDeleteInfoJSONFilesForVideo(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	downloader := NewDownloader(config)

	videoID := "abc123"
	matching := filepath.Join(tmpDir, "my-video-abc123.info.json")
	other := filepath.Join(tmpDir, "my-video-other999.info.json")

	if err := os.WriteFile(matching, []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile(matching) error = %v", err)
	}
	if err := os.WriteFile(other, []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}

	if err := downloader.deleteInfoJSONFilesForVideo(tmpDir, videoID); err != nil {
		t.Fatalf("deleteInfoJSONFilesForVideo() error = %v", err)
	}

	if _, err := os.Stat(matching); !os.IsNotExist(err) {
		t.Fatalf("expected matching info json to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("expected non-matching info json to remain, stat err = %v", err)
	}
}

func TestMigrateUnknownVideosMovesFilesToUploaderDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	unknownDir := filepath.Join(tmpDir, sanitizeFilename("unknown"))
	if err := os.MkdirAll(unknownDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	videoID := "abc123"
	prefix := "My Video-" + videoID
	infoPath := filepath.Join(unknownDir, prefix+".info.json")
	videoPath := filepath.Join(unknownDir, prefix+".mp4")
	nfoPath := filepath.Join(unknownDir, prefix+".nfo")
	thumbPath := filepath.Join(unknownDir, prefix+".jpg")

	infoJSON := `{"id":"abc123","uploader":"Channel One","title":"My Video"}`
	for path, content := range map[string]string{
		infoPath:  infoJSON,
		videoPath: "video",
		nfoPath:   "nfo",
		thumbPath: "jpg",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	migratedVideos, movedFiles, errs := downloader.MigrateUnknownVideos()
	if migratedVideos != 1 {
		t.Fatalf("expected 1 migrated video, got %d", migratedVideos)
	}
	if movedFiles != 4 {
		t.Fatalf("expected 4 moved files, got %d", movedFiles)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no migration warnings, got %v", errs)
	}

	targetDir := filepath.Join(tmpDir, sanitizeFilename("Channel One"))
	for _, movedPath := range []string{
		filepath.Join(targetDir, prefix+".info.json"),
		filepath.Join(targetDir, prefix+".mp4"),
		filepath.Join(targetDir, prefix+".nfo"),
		filepath.Join(targetDir, prefix+".jpg"),
	} {
		if _, err := os.Stat(movedPath); err != nil {
			t.Fatalf("expected moved file to exist: %s (err=%v)", movedPath, err)
		}
	}

	if _, err := os.Stat(videoPath); !os.IsNotExist(err) {
		t.Fatalf("expected source video file to be moved, stat err = %v", err)
	}
}

func TestMigrateUnknownVideosSkipsWhenUploaderMissing(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	unknownDir := filepath.Join(tmpDir, sanitizeFilename("unknown"))
	if err := os.MkdirAll(unknownDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	videoID := "no-uploader-001"
	prefix := "No Uploader-" + videoID
	infoPath := filepath.Join(unknownDir, prefix+".info.json")
	videoPath := filepath.Join(unknownDir, prefix+".mp4")

	infoJSON := `{"id":"no-uploader-001","title":"No Uploader"}`
	if err := os.WriteFile(infoPath, []byte(infoJSON), 0644); err != nil {
		t.Fatalf("WriteFile(infoPath) error = %v", err)
	}
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("WriteFile(videoPath) error = %v", err)
	}

	migratedVideos, movedFiles, errs := downloader.MigrateUnknownVideos()
	if migratedVideos != 0 {
		t.Fatalf("expected 0 migrated videos, got %d", migratedVideos)
	}
	if movedFiles != 0 {
		t.Fatalf("expected 0 moved files, got %d", movedFiles)
	}
	if len(errs) == 0 {
		t.Fatal("expected migration warnings for missing uploader metadata")
	}

	if _, err := os.Stat(videoPath); err != nil {
		t.Fatalf("expected source video file to remain in unknown dir, stat err = %v", err)
	}
}

func TestExtractVideoChapters(t *testing.T) {
	raw := map[string]interface{}{
		"chapters": []interface{}{
			map[string]interface{}{
				"title":      "Intro",
				"start_time": 0.0,
				"end_time":   15.5,
			},
			map[string]interface{}{
				"title":      "Main Topic",
				"start_time": 15.5,
				"end_time":   120.0,
			},
		},
	}

	chapters := extractVideoChapters(raw)
	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(chapters))
	}

	if chapters[0].Title != "Intro" || chapters[0].StartTime != 0.0 || chapters[0].EndTime != 15.5 {
		t.Fatalf("unexpected first chapter: %#v", chapters[0])
	}
	if chapters[1].Title != "Main Topic" || chapters[1].StartTime != 15.5 || chapters[1].EndTime != 120.0 {
		t.Fatalf("unexpected second chapter: %#v", chapters[1])
	}
}

func TestGenerateNFOFileIncludesChapters(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	downloader := NewDownloader(config)

	videoID := "chapters123"
	videoPath := filepath.Join(tmpDir, "Example Title-"+videoID+".mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("WriteFile(video) error = %v", err)
	}

	metadata := &VideoMetadata{
		ID:          videoID,
		Title:       "Example Title",
		Description: "Example Description",
		Uploader:    "Example Channel",
		UploadDate:  "2026-03-24",
		Duration:    300,
		Chapters: []VideoChapter{
			{Title: "Intro", StartTime: 0, EndTime: 30},
			{Title: "Deep Dive", StartTime: 30, EndTime: 300},
		},
	}

	if err := downloader.generateNFOFile(tmpDir, metadata); err != nil {
		t.Fatalf("generateNFOFile() error = %v", err)
	}

	nfoPath := filepath.Join(tmpDir, "Example Title-"+videoID+".nfo")
	contentBytes, err := os.ReadFile(nfoPath)
	if err != nil {
		t.Fatalf("ReadFile(nfo) error = %v", err)
	}
	content := string(contentBytes)

	checks := []string{
		"<chapters>",
		"<title>Intro</title>",
		"<start>0.000</start>",
		"<end>30.000</end>",
		"<title>Deep Dive</title>",
		"<end>300.000</end>",
	}

	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Fatalf("expected NFO to contain %q, got:\n%s", check, content)
		}
	}
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

	if err := downloader.CleanOldVideosForChannel(channelName, "channel-1", 7, time.Time{}, storage); err != nil {
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

func TestCleanOldVideosForChannelRespectsDownloadedVideoDisablePruning(t *testing.T) {
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

	keptOld := filepath.Join(channelDir, "tracked-old-keep001.mp4")
	if err := os.WriteFile(keptOld, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", keptOld, err)
	}

	oldTime := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(keptOld, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes(keptOld) error = %v", err)
	}

	err = storage.AddChannel(Channel{
		ID:   "channel-keep",
		Name: channelName,
		DownloadedVideos: []DownloadedVideo{
			{ID: "keep001", Title: "Keep me", DownloadDate: oldTime, DisablePruning: true},
		},
	})
	if err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	if err := downloader.CleanOldVideosForChannel(channelName, "channel-keep", 7, time.Time{}, storage); err != nil {
		t.Fatalf("CleanOldVideosForChannel() error = %v", err)
	}

	if _, err := os.Stat(keptOld); err != nil {
		t.Fatalf("expected kept old file to remain, stat err = %v", err)
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

	removed, err := downloader.CleanOldVideosForVideo("Tracked video entry", "video-entry-1", 7, storage)
	if err != nil {
		t.Fatalf("CleanOldVideosForVideo() error = %v", err)
	}
	if !removed {
		t.Fatalf("expected entry removal signal when old tracked file is pruned")
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

func TestRemoveChannelResourcesRemovesChannelDirectoryAndTrackedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	d := NewDownloader(cfg)

	channelName := "My Channel"
	channelDir := filepath.Join(tmpDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	tracked1 := filepath.Join(channelDir, "title-abc123.mp4")
	tracked2 := filepath.Join(tmpDir, "Elsewhere", "note-abc123.nfo")
	untracked := filepath.Join(tmpDir, "Elsewhere", "other-zzz999.mp4")
	if err := os.MkdirAll(filepath.Dir(tracked2), 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	for _, p := range []string{tracked1, tracked2, untracked} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("write failed for %s: %v", p, err)
		}
	}

	channel := Channel{
		ID:   "UC123",
		Name: channelName,
		DownloadedVideos: []DownloadedVideo{
			{ID: "abc123", Title: "Tracked"},
		},
	}

	if err := d.RemoveChannelResources(channel); err != nil {
		t.Fatalf("RemoveChannelResources() error = %v", err)
	}

	if _, err := os.Stat(channelDir); !os.IsNotExist(err) {
		t.Fatalf("expected channel directory removed, stat err = %v", err)
	}
	if _, err := os.Stat(tracked2); !os.IsNotExist(err) {
		t.Fatalf("expected tracked file removed, stat err = %v", err)
	}
	if _, err := os.Stat(untracked); err != nil {
		t.Fatalf("expected untracked file to remain, stat err = %v", err)
	}
}

func TestRemoveVideoResourcesRemovesTrackedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	d := NewDownloader(cfg)

	uploaderDir := filepath.Join(tmpDir, "Uploader")
	if err := os.MkdirAll(uploaderDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	tracked1 := filepath.Join(uploaderDir, "my-title-vid111.mp4")
	tracked2 := filepath.Join(uploaderDir, "my-title-vid222.nfo")
	untracked := filepath.Join(uploaderDir, "my-title-keep999.mp4")
	for _, p := range []string{tracked1, tracked2, untracked} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("write failed for %s: %v", p, err)
		}
	}

	v := Video{
		ID: "vid111",
		DownloadedVideos: []DownloadedVideo{
			{ID: "vid222", Title: "Tracked2"},
		},
	}

	if err := d.RemoveVideoResources(v); err != nil {
		t.Fatalf("RemoveVideoResources() error = %v", err)
	}

	if _, err := os.Stat(tracked1); !os.IsNotExist(err) {
		t.Fatalf("expected tracked1 removed, stat err = %v", err)
	}
	if _, err := os.Stat(tracked2); !os.IsNotExist(err) {
		t.Fatalf("expected tracked2 removed, stat err = %v", err)
	}
	if _, err := os.Stat(untracked); err != nil {
		t.Fatalf("expected untracked file to remain, stat err = %v", err)
	}
}

func TestChannelCutoffBasedPruning(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	storagePath := filepath.Join(tmpDir, "storage.json")
	storage, err := NewStorage(storagePath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channelName := "Channel with Cutoff"
	channelDir := filepath.Join(tmpDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	now := time.Now()
	cutoffDate := now.AddDate(0, 0, -5) // Cutoff 5 days ago

	// File downloaded 2 days ago
	recentFile := filepath.Join(channelDir, "recent-vid001.mp4")
	if err := os.WriteFile(recentFile, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	recentDownloadDate := now.AddDate(0, 0, -2)
	if err := os.Chtimes(recentFile, recentDownloadDate, recentDownloadDate); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	// File downloaded 10 days ago (old by retention) with publish date older than cutoff
	oldFile := filepath.Join(channelDir, "old-vid002.mp4")
	if err := os.WriteFile(oldFile, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	oldDownloadDate := now.AddDate(0, 0, -10)
	oldPublishDate := now.AddDate(0, 0, -7) // Published 7 days ago (before cutoff)
	if err := os.Chtimes(oldFile, oldDownloadDate, oldDownloadDate); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	// File published 6 days ago (before cutoff) but downloaded recently – should be pruned
	cutoffViolationFile := filepath.Join(channelDir, "cutoff-vid003.mp4")
	if err := os.WriteFile(cutoffViolationFile, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cutoffPublishDate := now.AddDate(0, 0, -6)   // Published 6 days ago
	recentDownloadDate2 := now.AddDate(0, 0, -1) // Downloaded 1 day ago
	if err := os.Chtimes(cutoffViolationFile, recentDownloadDate2, recentDownloadDate2); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	if err := storage.AddChannel(Channel{
		ID:   "channel-cutoff",
		Name: channelName,
		DownloadedVideos: []DownloadedVideo{
			{ID: "vid001", Title: "Recent", DownloadDate: recentDownloadDate, PublishDate: now},
			{ID: "vid002", Title: "Old", DownloadDate: oldDownloadDate, PublishDate: oldPublishDate},
			{ID: "vid003", Title: "Cutoff Violation", DownloadDate: recentDownloadDate2, PublishDate: cutoffPublishDate},
		},
	}); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	// Run cleanup with 7-day retention and cutoff 5 days ago
	if err := downloader.CleanOldVideosForChannel(channelName, "channel-cutoff", 7, cutoffDate, storage); err != nil {
		t.Fatalf("CleanOldVideosForChannel() error = %v", err)
	}

	// Recent file should remain (recent download and publish after cutoff)
	if _, err := os.Stat(recentFile); err != nil {
		t.Fatalf("expected recent file to remain, stat err = %v", err)
	}

	// Old file should be pruned (10 days old download, retention is 7 days)
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("expected old file (10 days old download) to be removed by retention, stat err = %v", err)
	}

	// Cutoff violation file should be removed (publish date is before cutoff)
	if _, err := os.Stat(cutoffViolationFile); !os.IsNotExist(err) {
		t.Fatalf("expected cutoff violation file (published before cutoff) to be removed, stat err = %v", err)
	}
}

func TestSingleEntryPruningReturnsRemovalSignal(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	storagePath := filepath.Join(tmpDir, "storage.json")
	storage, err := NewStorage(storagePath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	videoDir := filepath.Join(tmpDir, "Video Creator")
	if err := os.MkdirAll(videoDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create tracked old file
	oldFile := filepath.Join(videoDir, "old-track-abc111.mp4")
	if err := os.WriteFile(oldFile, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	now := time.Now()
	oldTime := now.AddDate(0, 0, -10)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	if err := storage.AddVideo(Video{
		ID:    "single-video-1",
		Title: "Video Creator",
		DownloadedVideos: []DownloadedVideo{
			{ID: "abc111", Title: "Old video", DownloadDate: oldTime},
		},
	}); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	// Call cleanup with very low retention to trigger prune
	removed, err := downloader.CleanOldVideosForVideo("Video Creator", "single-video-1", 3, storage)
	if err != nil {
		t.Fatalf("CleanOldVideosForVideo() error = %v", err)
	}

	// Since all tracked files are old, removal signal should be true
	if !removed {
		t.Fatalf("expected removal signal=true when all tracked files are pruned")
	}

	// File should be deleted
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, stat err = %v", err)
	}
}

func TestSingleEntryNominalRetentionDoesNotRemove(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	storagePath := filepath.Join(tmpDir, "storage.json")
	storage, err := NewStorage(storagePath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	videoDir := filepath.Join(tmpDir, "Creator")
	if err := os.MkdirAll(videoDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	recentFile := filepath.Join(videoDir, "recent-xyz123.mp4")
	if err := os.WriteFile(recentFile, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	now := time.Now()
	recentTime := now.AddDate(0, 0, -2)
	if err := os.Chtimes(recentFile, recentTime, recentTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	if err := storage.AddVideo(Video{
		ID:    "single-video-2",
		Title: "Creator",
		DownloadedVideos: []DownloadedVideo{
			{ID: "xyz123", Title: "Recent", DownloadDate: recentTime},
		},
	}); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	// Call cleanup with 7-day retention – file is only 2 days old
	removed, err := downloader.CleanOldVideosForVideo("Creator", "single-video-2", 7, storage)
	if err != nil {
		t.Fatalf("CleanOldVideosForVideo() error = %v", err)
	}

	// No files pruned, so removal signal should be false
	if removed {
		t.Fatalf("expected removal signal=false when no files are pruned")
	}

	// File should remain
	if _, err := os.Stat(recentFile); err != nil {
		t.Fatalf("expected file to remain, stat err = %v", err)
	}
}
