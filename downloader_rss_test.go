package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func withMockYouTubeRSSTransport(t *testing.T, fn roundTripFunc) {
	t.Helper()

	previous := http.DefaultTransport
	http.DefaultTransport = fn
	t.Cleanup(func() {
		http.DefaultTransport = previous
	})
}

func TestExtractChannelID(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantID    string
		wantError bool
	}{
		{
			name:   "standard channel format",
			url:    "https://www.youtube.com/channel/UC1234567890abcdef",
			wantID: "UC1234567890abcdef",
		},
		{
			name:   "channel URL with path",
			url:    "https://youtube.com/channel/UCtest123/videos",
			wantID: "UCtest123",
		},
		{
			name:      "custom handle (not supported)",
			url:       "https://www.youtube.com/@customhandle",
			wantError: true,
		},
		{
			name:      "invalid URL",
			url:       "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractChannelID(tt.url)
			if (err != nil) != tt.wantError {
				t.Errorf("extractChannelID() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if !tt.wantError && got != tt.wantID {
				t.Errorf("extractChannelID() = %v, want %v", got, tt.wantID)
			}
		})
	}
}

func TestResolveRSSChannelID(t *testing.T) {
	t.Run("uses stored canonical UC id", func(t *testing.T) {
		id, err := resolveRSSChannelID("UCstored123", "https://www.youtube.com/@handle")
		if err != nil {
			t.Fatalf("resolveRSSChannelID() error = %v", err)
		}
		if id != "UCstored123" {
			t.Fatalf("resolveRSSChannelID() = %q, want %q", id, "UCstored123")
		}
	})

	t.Run("falls back to URL extraction for legacy id", func(t *testing.T) {
		id, err := resolveRSSChannelID("@legacy", "https://www.youtube.com/channel/UCfromurl123")
		if err != nil {
			t.Fatalf("resolveRSSChannelID() error = %v", err)
		}
		if id != "UCfromurl123" {
			t.Fatalf("resolveRSSChannelID() = %q, want %q", id, "UCfromurl123")
		}
	})
}

func TestExtractVideoIDFromRSSEntry(t *testing.T) {
	tests := []struct {
		name   string
		entry  RSSEntry
		wantID string
	}{
		{
			name: "with dedicated videoId field",
			entry: RSSEntry{
				VideoID: "dQw4w9WgXcQ",
				ID:      "yt:video:other",
			},
			wantID: "dQw4w9WgXcQ",
		},
		{
			name: "parse from ID field",
			entry: RSSEntry{
				ID: "yt:video:abc123def456",
			},
			wantID: "abc123def456",
		},
		{
			name: "empty entry",
			entry: RSSEntry{
				ID:      "",
				VideoID: "",
			},
			wantID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVideoIDFromRSSEntry(tt.entry)
			if got != tt.wantID {
				t.Errorf("extractVideoIDFromRSSEntry() = %v, want %v", got, tt.wantID)
			}
		})
	}
}

func TestIsShortYouTubeURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "standard watch url",
			url:  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want: false,
		},
		{
			name: "shorts url",
			url:  "https://www.youtube.com/shorts/dQw4w9WgXcQ",
			want: true,
		},
		{
			name: "mobile shorts url",
			url:  "https://m.youtube.com/shorts/dQw4w9WgXcQ",
			want: true,
		},
		{
			name: "short link host",
			url:  "https://youtu.be/dQw4w9WgXcQ",
			want: false,
		},
		{
			name: "invalid url",
			url:  "://bad-url",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isShortYouTubeURL(tt.url)
			if got != tt.want {
				t.Errorf("isShortYouTubeURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestIsShortRSSEntry(t *testing.T) {
	tests := []struct {
		name  string
		entry RSSEntry
		want  bool
	}{
		{
			name: "shorts id prefix",
			entry: RSSEntry{
				ID: "yt:shorts:abc123",
			},
			want: true,
		},
		{
			name: "shorts alternate link",
			entry: RSSEntry{
				ID: "yt:video:abc123",
				Links: []RSSLink{
					{Rel: "alternate", Href: "https://www.youtube.com/shorts/abc123"},
				},
			},
			want: true,
		},
		{
			name: "regular watch link",
			entry: RSSEntry{
				ID: "yt:video:abc123",
				Links: []RSSLink{
					{Rel: "alternate", Href: "https://www.youtube.com/watch?v=abc123"},
				},
			},
			want: false,
		},
		{
			name: "no links",
			entry: RSSEntry{
				ID: "yt:video:abc123",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isShortRSSEntry(tt.entry)
			if got != tt.want {
				t.Errorf("isShortRSSEntry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetChannelVideosFromRSS(t *testing.T) {
	rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:yt="http://www.youtube.com/xml/schemas/2015/metadata">
  <entry>
    <id>yt:video:regular001</id>
    <title>Regular Video</title>
    <published>2026-03-16T10:00:00Z</published>
    <yt:videoId>regular001</yt:videoId>
    <link rel="alternate" href="https://www.youtube.com/watch?v=regular001"/>
  </entry>
  <entry>
    <id>yt:video:short001</id>
    <title>Short Video</title>
    <published>2026-03-16T11:00:00Z</published>
    <yt:videoId>short001</yt:videoId>
    <link rel="alternate" href="https://www.youtube.com/shorts/short001"/>
  </entry>
  <entry>
    <id>yt:shorts:short-by-id</id>
    <title>Short by ID Prefix</title>
    <published>2026-03-16T12:00:00Z</published>
    <yt:videoId>short-by-id</yt:videoId>
  </entry>
  <entry>
    <id>yt:video:old001</id>
    <title>Old Video</title>
    <published>2026-03-15T10:00:00Z</published>
    <yt:videoId>old001</yt:videoId>
    <link rel="alternate" href="https://www.youtube.com/watch?v=old001"/>
  </entry>
</feed>`

	withMockYouTubeRSSTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", req.Method)
		}
		if req.URL.Host != "www.youtube.com" {
			t.Fatalf("expected host www.youtube.com, got %s", req.URL.Host)
		}
		if req.URL.Path != "/feeds/videos.xml" {
			t.Fatalf("expected path /feeds/videos.xml, got %s", req.URL.Path)
		}
		if req.URL.Query().Get("channel_id") != "UCtest123" {
			t.Fatalf("expected channel_id UCtest123, got %s", req.URL.Query().Get("channel_id"))
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(rssXML)),
		}, nil
	})

	downloader := NewDownloader(DefaultConfig())
	since := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)

	t.Run("returns all recent videos with IsShort flagged, applying publish-date cutoff", func(t *testing.T) {
		videos, err := downloader.GetChannelVideosFromRSS(
			"UCtest123",
			"https://www.youtube.com/channel/UCtest123",
			since,
		)
		if err != nil {
			t.Fatalf("GetChannelVideosFromRSS() error = %v", err)
		}

		if len(videos) != 3 {
			t.Fatalf("expected 3 recent videos (regular + 2 shorts), got %d", len(videos))
		}

		byID := map[string]VideoInfo{}
		for _, v := range videos {
			byID[v.ID] = v
		}

		for _, wantID := range []string{"regular001", "short001", "short-by-id"} {
			if _, ok := byID[wantID]; !ok {
				t.Fatalf("expected video ID %s in results", wantID)
			}
		}
		if byID["regular001"].IsShort {
			t.Fatal("regular001 should not be marked as a short")
		}
		if !byID["short001"].IsShort {
			t.Fatal("short001 should be marked as a short")
		}
		if !byID["short-by-id"].IsShort {
			t.Fatal("short-by-id should be marked as a short")
		}
	})

	t.Run("returns error on non-200 response", func(t *testing.T) {
		withMockYouTubeRSSTransport(t, func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
			}, nil
		})

		_, err := downloader.GetChannelVideosFromRSS(
			"UCtest123",
			"https://www.youtube.com/channel/UCtest123",
			since,
		)
		if err == nil {
			t.Fatal("expected error for non-200 RSS response")
		}
		if !strings.Contains(err.Error(), "status 502") {
			t.Fatalf("expected status 502 error, got %v", err)
		}
	})
}
