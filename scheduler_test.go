package main

import (
	"testing"
	"time"
)

func TestEffectiveRetentionDays(t *testing.T) {
	if got := effectiveRetentionDays(14, 7); got != 14 {
		t.Fatalf("expected item retention to win, got %d", got)
	}

	if got := effectiveRetentionDays(0, 7); got != 7 {
		t.Fatalf("expected default retention fallback, got %d", got)
	}
}

func TestNormalizeChannelVideoURL(t *testing.T) {
	t.Run("raw id gets watch URL", func(t *testing.T) {
		got := normalizeChannelVideoURL("-0hOASBTWB4")
		want := "https://www.youtube.com/watch?v=-0hOASBTWB4"
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})

	t.Run("existing URL is preserved", func(t *testing.T) {
		input := "https://www.youtube.com/watch?v=-0hOASBTWB4"
		got := normalizeChannelVideoURL(input)
		if got != input {
			t.Fatalf("expected %q, got %q", input, got)
		}
	})
}

func TestExtractYouTubeVideoID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "raw id", input: "-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "watch url", input: "https://www.youtube.com/watch?v=-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "short url", input: "https://youtu.be/-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "shorts url", input: "https://www.youtube.com/shorts/-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "unknown url", input: "https://example.com/watch?v=abc", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractYouTubeVideoID(tc.input)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestBuildChannelSinceTime(t *testing.T) {
	now := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)

	t.Run("uses retention threshold when no cutoff", func(t *testing.T) {
		since := buildChannelSinceTime(now, 7, time.Time{})
		expected := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})

	t.Run("uses stricter cutoff when later than retention", func(t *testing.T) {
		cutoff := time.Date(2026, 3, 20, 18, 30, 0, 0, time.UTC)
		since := buildChannelSinceTime(now, 30, cutoff)
		expected := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})

	t.Run("uses retention when cutoff is older", func(t *testing.T) {
		cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		since := buildChannelSinceTime(now, 7, cutoff)
		expected := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})

	t.Run("retention dominates older cutoff example", func(t *testing.T) {
		exampleNow := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)
		cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		since := buildChannelSinceTime(exampleNow, 3, cutoff)
		expected := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})
}
