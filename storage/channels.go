package storage

import (
	"time"
)

// HasChannel returns true if a channel with the given canonical YouTube ID exists.
func (s *Storage) HasChannel(id string) bool {
	_, ok, err := resolveChannelPK(s.db, id)
	return err == nil && ok
}

// AddChannel adds a new channel, along with any pre-populated DownloadedVideos,
// FeedVideos, or PrunedVideos it carries (e.g. from converting individually-tracked
// videos into a new channel).
func (s *Storage) AddChannel(channel Channel) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := importChannel(tx, channel); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// RemoveChannel removes a channel by ID, along with every video linked to it (matching
// the old flat-JSON model, where a channel's downloaded/feed/pruned videos were embedded
// directly in the Channel struct and vanished with it). channel_sources cascades
// automatically via its FK to channels; the linked videos do not, since videos has no
// direct FK to channels (only the channel_videos join table does), so they are deleted
// explicitly here -- which then cascades their own video_sources/channel_videos rows.
func (s *Storage) RemoveChannel(id string) error {
	pk, ok, err := resolveChannelPK(s.db, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM videos WHERE id IN (SELECT video_id FROM channel_videos WHERE channel_id = ?)`, pk); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM channels WHERE id = ?`, pk); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// UpdateChannelLastChecked updates the last checked time for a channel.
func (s *Storage) UpdateChannelLastChecked(id string, t time.Time) error {
	_, err := s.db.Exec(`UPDATE channels SET last_checked = ?
		WHERE id = (SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?)`,
		timeToNull(t), sourceYouTube, id)
	return err
}

// MarkChannelBacklogScanComplete records that channelID has successfully fetched feed
// data at least once, so future scans use the bounded retention window instead of the
// wide cutoff-based backlog window. Only call this after a scan actually succeeds.
func (s *Storage) MarkChannelBacklogScanComplete(channelID string) error {
	_, err := s.db.Exec(`UPDATE channels SET backlog_scan_complete = 1
		WHERE id = (SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?)
		AND backlog_scan_complete = 0`,
		sourceYouTube, channelID)
	return err
}

// UpdateChannel updates retention days, pruning behavior, cutoff date, video quality,
// video format, and shorts preference for a channel.
func (s *Storage) UpdateChannel(id string, retentionDays int, disablePruning bool, cutoffDate time.Time, videoQuality, videoFormat string, downloadShorts, skipAutoDownload bool) error {
	_, err := s.db.Exec(`UPDATE channels SET
		retention_days = ?, disable_pruning = ?, cutoff_date = ?, video_quality = ?,
		video_format = ?, download_shorts = ?, skip_auto_download = ?
		WHERE id = (SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?)`,
		retentionDays, disablePruning, timeToNull(cutoffDate), videoQuality, videoFormat,
		downloadShorts, skipAutoDownload, sourceYouTube, id)
	return err
}

// UpsertFeedVideo adds or updates a video in the channel's pending list. ManualDownloadOnly
// is preserved from the existing entry so that a duration-based skip flag is not
// overwritten when the RSS feed re-discovers the same video. The update only applies to
// rows still in 'pending' status, guarding against ever regressing an already
// downloaded/pruned video back to pending.
func (s *Storage) UpsertFeedVideo(channelID string, video FeedVideo) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	channelPK, ok, err := resolveChannelPK(tx, channelID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	existingPK, ok, err := resolveVideoPK(tx, video.ID)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
			sourceVideoID: video.ID, title: video.Title, publishDate: video.PublishedAt,
			status: "pending", addedAt: video.AddedAt, isShort: video.IsShort,
			manualDownloadOnly: video.ManualDownloadOnly, url: video.URL,
		}); err != nil {
			return err
		}
		return tx.Commit()
	}

	res, err := tx.Exec(`UPDATE videos SET
		title = ?, publish_date = ?, added_at = ?, is_short = ?,
		manual_download_only = manual_download_only OR ?
		WHERE id = ? AND status = 'pending'`,
		video.Title, timeToNull(video.PublishedAt), timeToNull(video.AddedAt), video.IsShort,
		video.ManualDownloadOnly, existingPK)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows > 0 {
		if _, err := tx.Exec(`UPDATE video_sources SET url = ? WHERE video_id = ? AND source_id = ?`,
			nullableURL(video.URL), existingPK, sourceYouTube); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableURL(url string) any {
	if url == "" {
		return nil
	}
	return url
}

// MarkFeedVideoManualOnly sets ManualDownloadOnly=true on a pending video entry,
// indicating it should not be auto-downloaded (e.g. because it's under 2 minutes).
func (s *Storage) MarkFeedVideoManualOnly(channelID, videoID string) error {
	_, err := s.db.Exec(`UPDATE videos SET manual_download_only = 1
		WHERE id = (SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?)
		AND id IN (SELECT video_id FROM channel_videos WHERE channel_id = (
			SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?
		))
		AND manual_download_only = 0`,
		sourceYouTube, videoID, sourceYouTube, channelID)
	return err
}

// RemoveFeedVideo removes a video from the channel's pending list.
func (s *Storage) RemoveFeedVideo(channelID, videoID string) error {
	_, err := s.db.Exec(`DELETE FROM videos WHERE id = (
		SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?
	) AND status = 'pending' AND id IN (
		SELECT video_id FROM channel_videos WHERE channel_id = (
			SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?
		)
	)`, sourceYouTube, videoID, sourceYouTube, channelID)
	return err
}

// PruneFeedVideos removes pending video entries published before the given cutoff time.
// This is a hard delete with no memory kept -- matching the original behavior, a video
// dropped here can legitimately resurface later (e.g. if retention widens).
func (s *Storage) PruneFeedVideos(channelID string, cutoff time.Time) error {
	_, err := s.db.Exec(`DELETE FROM videos WHERE status = 'pending' AND publish_date < ?
		AND id IN (SELECT video_id FROM channel_videos WHERE channel_id = (
			SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?
		))`,
		timeToNull(cutoff), sourceYouTube, channelID)
	return err
}

// AddPrunedVideo adds a video directly to a channel's pruned list without requiring it
// to have been downloaded first. Used for shorts rejected at download time so they are
// not re-discovered on the next scheduler run.
func (s *Storage) AddPrunedVideo(channelID, videoID string, publishDate time.Time) error {
	if videoID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	channelPK, ok, err := resolveChannelPK(tx, channelID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	// Dismissing a still-pending feed video is a common caller (e.g. the "dismiss"
	// action) transitioning an EXISTING row straight to pruned, not just a brand new
	// insert -- so an existing row must be checked by status, not treated as an
	// unconditional no-op, or the caller's follow-up RemoveFeedVideo call (which only
	// deletes 'pending' rows) would otherwise delete it before it's ever remembered.
	if videoPK, ok, err := resolveVideoPK(tx, videoID); err != nil {
		return err
	} else if ok {
		var status string
		if err := tx.QueryRow(`SELECT status FROM videos WHERE id = ?`, videoPK).Scan(&status); err != nil {
			return err
		}
		if status == "pruned" || status == "downloaded" {
			return nil // already handled
		}
		if _, err := tx.Exec(`UPDATE videos SET status = 'pruned', publish_date = COALESCE(publish_date, ?) WHERE id = ?`,
			timeToNull(publishDate), videoPK); err != nil {
			return err
		}
		return tx.Commit()
	}

	if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
		sourceVideoID: videoID, publishDate: publishDate, status: "pruned",
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateChannelID updates the cached canonical YouTube ID for a channel (used for
// migrations); the channel's own synthetic PK never changes.
func (s *Storage) UpdateChannelID(oldID, newID string) error {
	_, err := s.db.Exec(`UPDATE channel_sources SET source_channel_id = ?
		WHERE source_id = ? AND source_channel_id = ?`, newID, sourceYouTube, oldID)
	return err
}

// TrimPrunedVideos removes entries from a channel's pruned list whose publish dates
// predate the given since time (entries with no known publish date are never evicted).
// Those videos will never re-appear in feed discovery, so there is no reason to keep
// tracking them.
func (s *Storage) TrimPrunedVideos(channelID string, since time.Time) error {
	if since.IsZero() {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM videos WHERE status = 'pruned'
		AND publish_date IS NOT NULL AND publish_date < ?
		AND id IN (SELECT video_id FROM channel_videos WHERE channel_id = (
			SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?
		))`,
		timeToNull(since), sourceYouTube, channelID)
	return err
}

// SetChannelError sets the error message for a channel.
func (s *Storage) SetChannelError(id string, errMsg string) error {
	_, err := s.db.Exec(`UPDATE channels SET last_error = ?, last_error_time = ?
		WHERE id = (SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?)`,
		errMsg, timeToNull(time.Now()), sourceYouTube, id)
	return err
}

// ClearChannelError clears the error message for a channel.
func (s *Storage) ClearChannelError(id string) error {
	_, err := s.db.Exec(`UPDATE channels SET last_error = '', last_error_time = NULL
		WHERE id = (SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?)`,
		sourceYouTube, id)
	return err
}

// SetChannelThumbnailIfEmpty sets the channel thumbnail URL only if it is not already populated.
func (s *Storage) SetChannelThumbnailIfEmpty(id, url string) error {
	if url == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE channels SET thumbnail_url = ?
		WHERE id = (SELECT channel_id FROM channel_sources WHERE source_id = ? AND source_channel_id = ?)
		AND (thumbnail_url IS NULL OR thumbnail_url = '')`,
		url, sourceYouTube, id)
	return err
}
