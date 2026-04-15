package main

import "time"

// retentionCutoff returns now minus retentionDays.
func retentionCutoff(now time.Time, retentionDays int) time.Time {
	if retentionDays <= 0 {
		return time.Time{}
	}

	return now.AddDate(0, 0, -retentionDays)
}
