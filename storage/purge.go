package storage

import (
	"database/sql"
	"time"
)

// permanentBookkeepingGraceDays is the fixed, retention-independent grace period after
// which a channel-owned 'pruned' or 'downloaded' row becomes eligible for hard deletion
// by PurgeExpiredVideos. It exists to bound database growth while still holding the
// "never redownload a handled video" guarantee (see AddPrunedVideo) as tightly as
// practically possible.
//
// This is deliberately NOT derived from the channel's current retention_days the way an
// earlier version of this cleanup was (a real bug -- see AddPrunedVideo's doc comment):
// retention_days and cutoff_date are user-editable at any time, and a threshold computed
// from today's settings can be silently invalidated by an ordinary later settings tweak
// (e.g. raising retention), letting a video the app promised never to redownload
// resurface via RSS and actually get redownloaded. A fixed constant can't be invalidated
// that way -- it never changes just because a setting was edited.
//
// It is still not a mathematical proof of safety: a channel whose retention_days is set
// larger than this many days could in principle still be actively discovering a video
// this old at the moment its bookkeeping is purged. Three years comfortably exceeds any
// retention window in practical, everyday use of this app, and pruned/downloaded rows
// are cheap enough (well under a KB each) that there's no real pressure to cut this any
// tighter than "very safe in practice."
const permanentBookkeepingGraceDays = 365 * 3

// PurgeExpiredVideos hard-deletes channel-owned video rows that are safely past the
// point of ever being needed again, in one of two ways:
//
//   - 'pending' rows more than 2x their effective retention window old: this is a
//     backstop against unbounded database growth for feed bookkeeping, which carries no
//     "never redownload" guarantee (losing one just means it may be freshly
//     re-evaluated on a later scan, which is fine). Under normal operation
//     PruneFeedVideos already retires these at roughly 1x the window on every channel
//     scan, so this only fires when that regular per-channel cleanup was skipped for an
//     extended period (e.g. a channel's scans have been failing).
//   - 'pruned' and 'downloaded' rows -- which DO carry that guarantee -- only once they
//     are either (a) older than the channel's own cutoff_date, which is a genuine,
//     retention-independent floor on the RSS discovery window (see BuildChannelSinceTime:
//     no matter how large retention_days is ever set, the window never reaches further
//     back than cutoff_date), or (b) older than permanentBookkeepingGraceDays, a fixed
//     horizon immune to any single settings change. Neither condition can be silently
//     invalidated by editing retention_days.
//
// In every case, rows are exempt when the channel or the video itself has pruning
// disabled.
//
// Standalone (individually-tracked) videos are not covered here: they are hard-deleted
// directly by RemoveVideo once past their own single retention window and never
// accumulate as lingering bookkeeping rows the way channel-owned videos can.
func (s *Storage) PurgeExpiredVideos(now time.Time, defaultRetentionDays int) (int64, error) {
	now = now.UTC()

	rows, err := s.db.Query(`
		SELECT v.id, v.status,
			CASE WHEN c.retention_days > 0 THEN c.retention_days ELSE ? END,
			c.cutoff_date,
			COALESCE(v.download_date, v.publish_date, v.added_at)
		FROM videos v
		JOIN channel_videos cv ON cv.video_id = v.id
		JOIN channels c ON c.id = cv.channel_id
		WHERE c.disable_pruning = 0 AND v.disable_pruning = 0
			AND v.status IN ('pending', 'pruned', 'downloaded')
	`, defaultRetentionDays)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	graceFloor := now.AddDate(0, 0, -permanentBookkeepingGraceDays)

	var expired []int64
	for rows.Next() {
		var id int64
		var status string
		var effectiveRetention int
		var cutoff sql.NullString
		var anchor sql.NullString
		if err := rows.Scan(&id, &status, &effectiveRetention, &cutoff, &anchor); err != nil {
			return 0, err
		}
		anchorTime := nullToTime(anchor)
		if anchorTime.IsZero() {
			continue
		}

		switch status {
		case "pending":
			if effectiveRetention <= 0 {
				continue
			}
			if anchorTime.Before(now.AddDate(0, 0, -2*effectiveRetention)) {
				expired = append(expired, id)
			}
		default: // "pruned", "downloaded"
			cutoffTime := nullToTime(cutoff)
			pastCutoff := !cutoffTime.IsZero() && anchorTime.Before(cutoffTime)
			pastGracePeriod := anchorTime.Before(graceFloor)
			if pastCutoff || pastGracePeriod {
				expired = append(expired, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`DELETE FROM videos WHERE id = ? AND status IN ('pending', 'pruned', 'downloaded')`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	var deleted int64
	for _, id := range expired {
		res, err := stmt.Exec(id)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += n
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if deleted > 0 {
		s.notify()
	}
	return deleted, nil
}
