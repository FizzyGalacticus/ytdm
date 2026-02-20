package main

import (
	"encoding/json"
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
