package storage

import (
	"strings"
	"time"
)

// HasVideo returns true if a standalone (individually-tracked) video entry with the
// given ID exists.
func (s *Storage) HasVideo(id string) bool {
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM individual_video_tracking ivt
		JOIN video_sources vs ON vs.video_id = ivt.video_id
		WHERE vs.source_id = ? AND vs.source_video_id = ?
	)`, sourceYouTube, id).Scan(&exists)
	return err == nil && exists
}

// AddVideo adds a new standalone (individually-tracked) video.
func (s *Storage) AddVideo(video Video) error {
	if video.AddedDate.IsZero() {
		video.AddedDate = time.Now().UTC()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := importStandaloneVideo(tx, video); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify()
	return nil
}

// RemoveVideo removes a standalone video entry by ID. Child rows (video_sources,
// individual_video_tracking) cascade automatically via foreign keys.
func (s *Storage) RemoveVideo(id string) error {
	res, err := s.db.Exec(`DELETE FROM videos WHERE id = (
		SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?
	) AND id IN (SELECT video_id FROM individual_video_tracking)`, sourceYouTube, id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows > 0 {
		s.notify()
	}
	return nil
}

// UpdateVideoLastChecked updates the last checked time for a standalone video.
func (s *Storage) UpdateVideoLastChecked(id string, t time.Time) error {
	_, err := s.db.Exec(`UPDATE videos SET last_checked = ? WHERE id = (
		SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?
	) AND id IN (SELECT video_id FROM individual_video_tracking)`, timeToNull(t), sourceYouTube, id)
	return err
}

// UpdateVideo updates retention, pruning behavior, quality, format, and shorts
// preference for a standalone video.
func (s *Storage) UpdateVideo(id string, retentionDays int, disablePruning bool, videoQuality, videoFormat string, downloadShorts bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	videoPK, ok, err := resolveVideoPK(tx, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if _, err := tx.Exec(`UPDATE videos SET disable_pruning = ? WHERE id = ?`, disablePruning, videoPK); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE individual_video_tracking SET
		retention_days = ?, video_quality = ?, video_format = ?, download_shorts = ?
		WHERE video_id = ?`,
		retentionDays, videoQuality, videoFormat, downloadShorts, videoPK); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateVideoUploaderInfo caches uploader metadata for a video to avoid re-querying yt-dlp.
func (s *Storage) UpdateVideoUploaderInfo(id, uploader, uploaderID string) error {
	_, err := s.db.Exec(`UPDATE video_sources SET uploader_name = ?, uploader_source_channel_id = ?
		WHERE source_id = ? AND source_video_id = ?`,
		strings.TrimSpace(uploader), strings.TrimSpace(uploaderID), sourceYouTube, id)
	return err
}

// SetVideoError sets the error message for a standalone video.
func (s *Storage) SetVideoError(id string, errMsg string) error {
	_, err := s.db.Exec(`UPDATE videos SET last_error = ?, last_error_time = ? WHERE id = (
		SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?
	) AND id IN (SELECT video_id FROM individual_video_tracking)`,
		errMsg, timeToNull(time.Now()), sourceYouTube, id)
	return err
}

// ClearVideoError clears the error message for a standalone video.
func (s *Storage) ClearVideoError(id string) error {
	_, err := s.db.Exec(`UPDATE videos SET last_error = '', last_error_time = NULL WHERE id = (
		SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?
	) AND id IN (SELECT video_id FROM individual_video_tracking)`, sourceYouTube, id)
	return err
}
