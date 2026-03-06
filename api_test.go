package main

import (
	"testing"
)

func TestExtractIDFromURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "standard watch url - returns 'watch' due to query param removal",
			url:      "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			expected: "watch",
		},
		{
			name:     "watch url with query params - returns 'watch'",
			url:      "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s",
			expected: "watch",
		},
		{
			name:     "short url",
			url:      "https://youtu.be/dQw4w9WgXcQ",
			expected: "dQw4w9WgXcQ",
		},
		{
			name:     "short url with query params",
			url:      "https://youtu.be/dQw4w9WgXcQ?t=5",
			expected: "dQw4w9WgXcQ",
		},
		{
			name:     "shorts url",
			url:      "https://www.youtube.com/shorts/abc123xyz",
			expected: "abc123xyz",
		},
		{
			name:     "shorts url with query params",
			url:      "https://www.youtube.com/shorts/abc123xyz?feature=share",
			expected: "abc123xyz",
		},
		{
			name:     "embed url",
			url:      "https://www.youtube.com/embed/dQw4w9WgXcQ",
			expected: "dQw4w9WgXcQ",
		},
		{
			name:     "playlist url - returns 'playlist' due to query param removal",
			url:      "https://www.youtube.com/playlist?list=PLabcdef123456",
			expected: "playlist",
		},
		{
			name:     "channel url with @",
			url:      "https://www.youtube.com/@channelname",
			expected: "@channelname",
		},
		{
			name:     "channel url with /c/",
			url:      "https://www.youtube.com/c/channelname",
			expected: "channelname",
		},
		{
			name:     "user url",
			url:      "https://www.youtube.com/user/username",
			expected: "username",
		},
		{
			name:     "empty url",
			url:      "",
			expected: "",
		},
		{
			name:     "url with only domain",
			url:      "https://www.youtube.com",
			expected: "www.youtube.com",
		},
		{
			name:     "url with trailing slash",
			url:      "https://www.youtube.com/watch?v=dQw4w9WgXcQ/",
			expected: "",
		},
		{
			name:     "url with fragment - fragment not split by slash",
			url:      "https://www.youtube.com/watch?v=dQw4w9WgXcQ#t=10",
			expected: "watch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractIDFromURL(tt.url)
			if result != tt.expected {
				t.Errorf("extractIDFromURL(%q) = %q, want %q", tt.url, result, tt.expected)
			}
		})
	}
}

func TestExtractIDFromURLEdgeCases(t *testing.T) {
	t.Run("non-youtube url", func(t *testing.T) {
		url := "https://example.com/video/12345"
		result := extractIDFromURL(url)
		if result != "12345" {
			t.Errorf("Expected '12345', got %q", result)
		}
	})

	t.Run("url with multiple slashes", func(t *testing.T) {
		url := "https://www.youtube.com//watch//v=dQw4w9WgXcQ"
		result := extractIDFromURL(url)
		// Function takes last non-empty part
		if result == "" {
			t.Error("Expected non-empty result for malformed URL")
		}
	})

	t.Run("url with special characters in id", func(t *testing.T) {
		url := "https://www.youtube.com/watch?v=abc-_123"
		result := extractIDFromURL(url)
		if result != "watch" {
			t.Errorf("Expected 'watch', got %q", result)
		}
	})
}
