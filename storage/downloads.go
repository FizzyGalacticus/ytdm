package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MarkVideoAsDownloaded marks a video as downloaded under the given container, which may
// be either a channel ID or a standalone video's own ID (checked in that order). It is
// idempotent: a video already marked downloaded is left untouched. If no existing row is
// found for videoID under a channel (a defensive path -- in practice a pending row always
// already exists), a new one is created and linked. For the standalone-video branch,
// videoID may differ slightly from the container's own tracked ID (yt-dlp can resolve a
// more precise canonical ID); when that happens the tracked source_video_id is corrected
// in place rather than creating a second row for what's really the same video.
func (s *Storage) MarkVideoAsDownloaded(containerID, videoID, videoTitle string, publishDate time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if channelPK, ok, err := resolveChannelPK(tx, containerID); err != nil {
		return err
	} else if ok {
		var existingPK int64
		var status string
		err := tx.QueryRow(`SELECT v.id, v.status FROM videos v
			JOIN video_sources vs ON vs.video_id = v.id
			JOIN channel_videos cv ON cv.video_id = v.id
			WHERE vs.source_id = ? AND vs.source_video_id = ? AND cv.channel_id = ?`,
			sourceYouTube, videoID, channelPK).Scan(&existingPK, &status)
		switch {
		case err == sql.ErrNoRows:
			if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
				sourceVideoID: videoID, title: videoTitle, publishDate: publishDate,
				status: "downloaded", downloadDate: time.Now().UTC(),
			}); err != nil {
				return err
			}
		case err != nil:
			return err
		case status == "downloaded":
			return nil // already marked
		default:
			if _, err := tx.Exec(`UPDATE videos SET status = 'downloaded', download_date = ?, title = ?, publish_date = ? WHERE id = ?`,
				timeToNull(time.Now().UTC()), videoTitle, timeToNull(publishDate), existingPK); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	// Standalone video: containerID is itself the tracked video's own ID.
	videoPK, ok, err := resolveVideoPK(tx, containerID)
	if err != nil {
		return err
	}
	if !ok {
		return nil // container not found at all
	}

	var status string
	if err := tx.QueryRow(`SELECT status FROM videos WHERE id = ?`, videoPK).Scan(&status); err != nil {
		return err
	}
	if status == "downloaded" {
		return nil
	}
	if _, err := tx.Exec(`UPDATE videos SET status = 'downloaded', download_date = ?, title = ?, publish_date = ? WHERE id = ?`,
		timeToNull(time.Now().UTC()), videoTitle, timeToNull(publishDate), videoPK); err != nil {
		return err
	}
	if videoID != containerID {
		if _, err := tx.Exec(`UPDATE video_sources SET source_video_id = ? WHERE video_id = ? AND source_id = ?`,
			videoID, videoPK, sourceYouTube); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IsVideoDownloaded checks if a video has been downloaded (or pruned, for channel-owned
// videos -- both count as "already handled") under the given container, which may be
// either a channel ID or a standalone video's own ID.
func (s *Storage) IsVideoDownloaded(containerID, videoID string) bool {
	if channelPK, ok, err := resolveChannelPK(s.db, containerID); err == nil && ok {
		var status string
		err := s.db.QueryRow(`SELECT v.status FROM videos v
			JOIN video_sources vs ON vs.video_id = v.id
			JOIN channel_videos cv ON cv.video_id = v.id
			WHERE vs.source_id = ? AND vs.source_video_id = ? AND cv.channel_id = ?`,
			sourceYouTube, videoID, channelPK).Scan(&status)
		if err != nil {
			return false
		}
		return status == "downloaded" || status == "pruned"
	}

	if videoPK, ok, err := resolveVideoPK(s.db, containerID); err == nil && ok {
		var isStandalone bool
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM individual_video_tracking WHERE video_id = ?)`, videoPK).Scan(&isStandalone); err != nil || !isStandalone {
			return false
		}
		var status string
		if err := s.db.QueryRow(`SELECT status FROM videos WHERE id = ?`, videoPK).Scan(&status); err != nil {
			return false
		}
		return status == "downloaded"
	}

	return false
}

// RemoveDownloadedVideo removes a specific video from a channel's or standalone video's
// downloaded state. For a channel-owned video this demotes it to 'pruned' (remembered
// forever so it is never re-downloaded); for a standalone video it is a hard delete,
// since standalone videos have no pruned-memory concept.
func (s *Storage) RemoveDownloadedVideo(containerID, videoID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if channelPK, ok, err := resolveChannelPK(tx, containerID); err != nil {
		return err
	} else if ok {
		var videoPK int64
		err := tx.QueryRow(`SELECT v.id FROM videos v
			JOIN video_sources vs ON vs.video_id = v.id
			JOIN channel_videos cv ON cv.video_id = v.id
			WHERE vs.source_id = ? AND vs.source_video_id = ? AND cv.channel_id = ? AND v.status = 'downloaded'`,
			sourceYouTube, videoID, channelPK).Scan(&videoPK)
		if err == sql.ErrNoRows {
			return nil // video not found in list
		}
		if err != nil {
			return err
		}
		if videoID != "" {
			if _, err := tx.Exec(`UPDATE videos SET status = 'pruned' WHERE id = ?`, videoPK); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`DELETE FROM videos WHERE id = ?`, videoPK); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	if videoPK, ok, err := resolveVideoPK(tx, containerID); err != nil {
		return err
	} else if ok {
		var isStandalone bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM individual_video_tracking WHERE video_id = ?)`, videoPK).Scan(&isStandalone); err != nil {
			return err
		}
		if isStandalone {
			if _, err := tx.Exec(`DELETE FROM videos WHERE id = ?`, videoPK); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// MergeChannelDownloadedVideos appends downloaded video entries into a channel's list.
// A video ID already linked to this same channel is left untouched (preserves the
// channel's own copy, e.g. if its own RSS scan independently discovered the same video
// around the same time). A video ID that exists elsewhere -- typically a standalone
// individually-tracked video being converted/merged into this channel -- is relinked
// here rather than skipped, so its download history isn't dropped during the move.
func (s *Storage) MergeChannelDownloadedVideos(id string, videos []DownloadedVideo) error {
	if len(videos) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	channelPK, ok, err := resolveChannelPK(tx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("channel %s not found", id)
	}

	for _, dv := range videos {
		if videoPK, ok, err := resolveVideoPK(tx, dv.ID); err != nil {
			return err
		} else if ok {
			var alreadyLinked bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM channel_videos WHERE channel_id = ? AND video_id = ?)`,
				channelPK, videoPK).Scan(&alreadyLinked); err != nil {
				return err
			}
			if alreadyLinked {
				continue // this channel already has its own copy
			}
		}
		if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
			sourceVideoID: dv.ID, title: dv.Title, publishDate: dv.PublishDate,
			status: "downloaded", downloadDate: dv.DownloadDate, disablePruning: dv.DisablePruning,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateChannelDownloadedVideoPruning updates per-downloaded-video pruning behavior for a
// channel entry.
func (s *Storage) UpdateChannelDownloadedVideoPruning(channelID, videoID string, disablePruning bool) error {
	channelPK, ok, err := resolveChannelPK(s.db, channelID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("channel %s not found", channelID)
	}

	res, err := s.db.Exec(`UPDATE videos SET disable_pruning = ? WHERE id = (
		SELECT video_id FROM video_sources WHERE source_id = ? AND source_video_id = ?
	) AND id IN (SELECT video_id FROM channel_videos WHERE channel_id = ?) AND status = 'downloaded'`,
		disablePruning, sourceYouTube, videoID, channelPK)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("downloaded video %s not found in channel %s", videoID, channelID)
	}
	return nil
}

// ReconcileDownloadedVideos removes downloaded-video rows that no longer have a
// corresponding media file on disk. channelDirName computes a channel's directory name
// from its display name (package main's sanitizeFilename) -- kept as a parameter since
// the storage package cannot import back from package main.
func (s *Storage) ReconcileDownloadedVideos(downloadDir string, channelDirName func(name string) string) error {
	var globalFileNames []string
	walkErr := filepath.Walk(downloadDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		globalFileNames = append(globalFileNames, info.Name())
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return walkErr
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	channelRows, err := tx.Query(`SELECT id, name FROM channels`)
	if err != nil {
		return err
	}
	type channel struct {
		pk   int64
		name string
	}
	var channels []channel
	for channelRows.Next() {
		var c channel
		if err := channelRows.Scan(&c.pk, &c.name); err != nil {
			channelRows.Close()
			return err
		}
		channels = append(channels, c)
	}
	channelRows.Close()

	for _, c := range channels {
		channelDir := filepath.Join(downloadDir, channelDirName(c.name))
		var channelFileNames []string
		if entries, err := os.ReadDir(channelDir); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					channelFileNames = append(channelFileNames, entry.Name())
				}
			}
		}

		stalePKs, err := staleDownloadedVideoPKs(tx, `SELECT v.id, vs.source_video_id FROM videos v
			JOIN video_sources vs ON vs.video_id = v.id
			JOIN channel_videos cv ON cv.video_id = v.id
			WHERE cv.channel_id = ? AND v.status = 'downloaded' AND vs.source_id = ?`,
			channelFileNames, c.pk, sourceYouTube)
		if err != nil {
			return err
		}
		for _, pk := range stalePKs {
			if _, err := tx.Exec(`DELETE FROM videos WHERE id = ?`, pk); err != nil {
				return err
			}
		}
	}

	staleStandalonePKs, err := staleDownloadedVideoPKs(tx, `SELECT v.id, vs.source_video_id FROM videos v
		JOIN video_sources vs ON vs.video_id = v.id
		JOIN individual_video_tracking ivt ON ivt.video_id = v.id
		WHERE v.status = 'downloaded' AND vs.source_id = ?`,
		globalFileNames, sourceYouTube)
	if err != nil {
		return err
	}
	for _, pk := range staleStandalonePKs {
		if _, err := tx.Exec(`DELETE FROM videos WHERE id = ?`, pk); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func staleDownloadedVideoPKs(tx *sql.Tx, query string, fileNames []string, args ...any) ([]int64, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []int64
	for rows.Next() {
		var pk int64
		var sourceVideoID string
		if err := rows.Scan(&pk, &sourceVideoID); err != nil {
			return nil, err
		}
		if !hasFileContainingID(fileNames, sourceVideoID) {
			stale = append(stale, pk)
		}
	}
	return stale, rows.Err()
}

func hasFileContainingID(fileNames []string, videoID string) bool {
	if videoID == "" {
		return false
	}
	for _, name := range fileNames {
		if strings.Contains(name, videoID) {
			return true
		}
	}
	return false
}
