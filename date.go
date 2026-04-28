package main

import (
	"fmt"
	"strings"
	"time"
)

func NormalizeToUTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}

	return t.UTC()
}

func ParseYouTubeUploadDateUTC(uploadDate string) (time.Time, error) {
	trimmed := strings.TrimSpace(uploadDate)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty upload date")
	}

	t, err := time.Parse("20060102", trimmed)
	if err != nil {
		return time.Time{}, err
	}

	return NormalizeToUTC(t), nil
}

// RetentionCutoff returns now minus retentionDays.
func RetentionCutoff(now time.Time, retentionDays int) time.Time {
	if retentionDays <= 0 {
		return time.Time{}
	}

	return NormalizeToUTC(now).AddDate(0, 0, -retentionDays)
}

func EffectiveRetentionDays(itemRetention, defaultRetention int) int {
	if itemRetention > 0 {
		return itemRetention
	}

	return defaultRetention
}

func BuildChannelSinceTime(now time.Time, retentionDays int, cutoffDate time.Time) time.Time {
	var since time.Time

	if retentionDays > 0 {
		retentionThreshold := RetentionCutoff(now, retentionDays)
		since = retentionThreshold.Add(-time.Second)
	}

	if !cutoffDate.IsZero() {
		cutoffSince := NormalizeToUTC(cutoffDate).Add(-time.Second)
		if since.IsZero() || cutoffSince.After(since) {
			since = cutoffSince
		}
	}

	return since
}

func ShouldPruneByChannelCutoff(publishDate, cutoffDate time.Time) bool {
	if cutoffDate.IsZero() || publishDate.IsZero() {
		return false
	}

	return NormalizeToUTC(publishDate).Before(NormalizeToUTC(cutoffDate))
}
