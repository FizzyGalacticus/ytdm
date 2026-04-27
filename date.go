package main

import "time"

// RetentionCutoff returns now minus retentionDays.
func RetentionCutoff(now time.Time, retentionDays int) time.Time {
	if retentionDays <= 0 {
		return time.Time{}
	}

	return now.AddDate(0, 0, -retentionDays)
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
		cutoffSince := cutoffDate.Add(-time.Second)
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

	return publishDate.Before(cutoffDate)
}
