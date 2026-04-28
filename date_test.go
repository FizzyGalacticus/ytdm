package main

import (
	"testing"
	"time"
)

func TestEffectiveRetentionDays(t *testing.T) {
	if got := EffectiveRetentionDays(14, 7); got != 14 {
		t.Fatalf("expected item retention to win, got %d", got)
	}

	if got := EffectiveRetentionDays(0, 7); got != 7 {
		t.Fatalf("expected default retention fallback, got %d", got)
	}
}

func TestBuildChannelSinceTime(t *testing.T) {
	now := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)

	t.Run("uses retention threshold when no cutoff", func(t *testing.T) {
		since := BuildChannelSinceTime(now, 7, time.Time{})
		expected := now.AddDate(0, 0, -7).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})

	t.Run("uses stricter cutoff when later than retention", func(t *testing.T) {
		cutoff := time.Date(2026, 3, 20, 18, 30, 0, 0, time.UTC)
		since := BuildChannelSinceTime(now, 30, cutoff)
		expected := cutoff.Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})

	t.Run("uses retention when cutoff is older", func(t *testing.T) {
		cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		since := BuildChannelSinceTime(now, 7, cutoff)
		expected := now.AddDate(0, 0, -7).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})

	t.Run("retention dominates older cutoff example", func(t *testing.T) {
		exampleNow := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)
		cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		since := BuildChannelSinceTime(exampleNow, 3, cutoff)
		expected := exampleNow.AddDate(0, 0, -3).Add(-time.Second)
		if !since.Equal(expected) {
			t.Fatalf("expected %v, got %v", expected, since)
		}
	})
}

func TestChannelEligibilityEnforcesBothCutoffAndRetention(t *testing.T) {
	now := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	retention := 7

	since := BuildChannelSinceTime(now, retention, cutoff)

	publishBeforeCutoff := time.Date(2026, 3, 19, 0, 0, 0, 0, time.UTC)
	if !publishBeforeCutoff.Before(since) {
		t.Fatalf("video published before cutoff should be before since threshold, got %v vs since %v", publishBeforeCutoff, since)
	}

	publishAtCutoff := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	if !publishAtCutoff.After(since) {
		t.Fatalf("video published at cutoff should be eligible (after since), got %v vs since %v", publishAtCutoff, since)
	}

	publishOldButInRetention := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)
	if !publishOldButInRetention.Before(since) {
		t.Fatalf("expected old but in-retention video (before cutoff) to be ineligible, got %v vs since %v", publishOldButInRetention, since)
	}
}

func TestStrictChannelRetentionWithoutCutoff(t *testing.T) {
	now := time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC)
	retention := 7

	since := BuildChannelSinceTime(now, retention, time.Time{})

	publishOld := now.AddDate(0, 0, -8)
	if !publishOld.Before(since) {
		t.Fatalf("video from 8 days ago should be ineligible with 7-day retention, got %v vs since %v", publishOld, since)
	}

	publishRecent := now.AddDate(0, 0, -2)
	if !publishRecent.After(since) {
		t.Fatalf("video from 2 days ago should be eligible with 7-day retention, got %v vs since %v", publishRecent, since)
	}
}

func TestShouldPruneByChannelCutoff(t *testing.T) {
	cutoff := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)

	t.Run("zero cutoff", func(t *testing.T) {
		if ShouldPruneByChannelCutoff(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC), time.Time{}) {
			t.Fatal("expected false when cutoff date is zero")
		}
	})

	t.Run("zero publish date", func(t *testing.T) {
		if ShouldPruneByChannelCutoff(time.Time{}, cutoff) {
			t.Fatal("expected false when publish date is zero")
		}
	})

	t.Run("before cutoff", func(t *testing.T) {
		if !ShouldPruneByChannelCutoff(time.Date(2026, 4, 19, 23, 59, 59, 0, time.UTC), cutoff) {
			t.Fatal("expected prune for publish date before cutoff")
		}
	})

	t.Run("at cutoff", func(t *testing.T) {
		if ShouldPruneByChannelCutoff(cutoff, cutoff) {
			t.Fatal("expected no prune when publish date equals cutoff")
		}
	})

	t.Run("after cutoff", func(t *testing.T) {
		if ShouldPruneByChannelCutoff(time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC), cutoff) {
			t.Fatal("expected no prune when publish date is after cutoff")
		}
	})

	t.Run("timezone normalized comparison", func(t *testing.T) {
		locPlus2 := time.FixedZone("UTC+2", 2*60*60)
		publishUTC := time.Date(2026, 4, 19, 22, 30, 0, 0, time.UTC)
		publishPlus2 := publishUTC.In(locPlus2)
		cutoffPlus2 := cutoff.In(locPlus2)

		if !ShouldPruneByChannelCutoff(publishPlus2, cutoffPlus2) {
			t.Fatal("expected prune decision to be consistent across timezones")
		}
	})
}

func TestParseYouTubeUploadDateUTC(t *testing.T) {
	got, err := ParseYouTubeUploadDateUTC("20260427")
	if err != nil {
		t.Fatalf("ParseYouTubeUploadDateUTC() error = %v", err)
	}

	expected := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	if !got.Equal(expected) {
		t.Fatalf("expected %v, got %v", expected, got)
	}
	if got.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", got.Location())
	}
}

func TestRetentionBoundaryOneDayAtSixPM(t *testing.T) {
	retentionDays := 1
	publishTime := time.Date(2026, 4, 1, 17, 59, 0, 0, time.UTC)

	nowBeforeRemoval := time.Date(2026, 4, 2, 17, 58, 59, 0, time.UTC)
	sinceBefore := BuildChannelSinceTime(nowBeforeRemoval, retentionDays, time.Time{})
	if !publishTime.After(sinceBefore) {
		t.Fatalf("expected publish time %v to still be eligible before retention boundary, since=%v", publishTime, sinceBefore)
	}

	nowAtOrAfterRemoval := time.Date(2026, 4, 2, 18, 0, 0, 0, time.UTC)
	sinceAfter := BuildChannelSinceTime(nowAtOrAfterRemoval, retentionDays, time.Time{})
	if !publishTime.Before(sinceAfter) {
		t.Fatalf("expected publish time %v to be ineligible on/after retention boundary, since=%v", publishTime, sinceAfter)
	}
}
