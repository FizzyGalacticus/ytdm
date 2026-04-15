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

func TestChannelEligibilityEnforcesBothCutoffAndRetention(t *testing.T) {
	now := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC) // Recent cutoff
	retention := 7

	since := buildChannelSinceTime(now, retention, cutoff)

	// The video published on 2026-03-19 is before cutoff (Mar 20), so should NOT be eligible
	publishBeforeCutoff := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
	if !publishBeforeCutoff.Before(since) {
		t.Fatalf("video published before cutoff should be before since threshold, got %v vs since %v", publishBeforeCutoff, since)
	}

	// A video published on 2026-03-20 is at cutoff, should be eligible (after since)
	publishAtCutoff := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	if !publishAtCutoff.After(since) {
		t.Fatalf("video published at cutoff should be eligible (after since), got %v vs since %v", publishAtCutoff, since)
	}

	// A video published within retention window but BEFORE cutoff should NOT be eligible
	publishOldButInRetention := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC) // Within 7 days but before cutoff
	if !publishOldButInRetention.Before(since) {
		t.Fatalf("expected old but in-retention video (before cutoff) to be ineligible, got %v vs since %v", publishOldButInRetention, since)
	}
}

func TestStrictChannelRetentionWithoutCutoff(t *testing.T) {
	now := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)
	retention := 7

	// No cutoff provided (zero time)
	since := buildChannelSinceTime(now, retention, time.Time{})

	// Video from 8 days ago should NOT be eligible (older than retention)
	publishOld := now.AddDate(0, 0, -8)
	if !publishOld.Before(since) {
		t.Fatalf("video from 8 days ago should be ineligible with 7-day retention, got %v vs since %v", publishOld, since)
	}

	// Video from 2 days ago should be eligible
	publishRecent := now.AddDate(0, 0, -2)
	if !publishRecent.After(since) {
		t.Fatalf("video from 2 days ago should be eligible with 7-day retention, got %v vs since %v", publishRecent, since)
	}
}
