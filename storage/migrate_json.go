package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// importLegacyJSONIfNeeded checks json_import_state; if not yet imported and a legacy
// JSON file exists at legacyPath, imports it in one transaction, marks the import
// complete, and best-effort renames the source file. Safe to call on every NewStorage
// invocation -- a no-op after the first successful run. Idempotency is governed entirely
// by the json_import_state flag, not by whether the tables happen to be empty, so a
// legitimately-emptied live database is never mistaken for "never imported."
func importLegacyJSONIfNeeded(db *sql.DB, legacyPath string) error {
	var alreadyImported bool
	if err := db.QueryRow(`SELECT imported FROM json_import_state WHERE id = 1`).Scan(&alreadyImported); err != nil {
		return err
	}
	if alreadyImported {
		return nil
	}

	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to import. Mark imported anyway so a legacy file dropped in later
			// (e.g. restored from an old backup) is never auto-imported over live data.
			_, err := db.Exec(`UPDATE json_import_state SET imported = 1, imported_at = ? WHERE id = 1`,
				time.Now().UTC().Format(time.RFC3339Nano))
			return err
		}
		return err
	}

	var legacy StorageData
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return fmt.Errorf("parsing legacy %s: %w", legacyPath, err)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit

	for _, ch := range legacy.Channels {
		if err := importChannel(tx, ch); err != nil {
			return fmt.Errorf("importing channel %s: %w", ch.ID, err)
		}
	}
	for _, v := range legacy.Videos {
		if err := importStandaloneVideo(tx, v); err != nil {
			return fmt.Errorf("importing video %s: %w", v.ID, err)
		}
	}

	if _, err := tx.Exec(`UPDATE json_import_state SET imported = 1, imported_at = ?, source_path = ? WHERE id = 1`,
		time.Now().UTC().Format(time.RFC3339Nano), legacyPath); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Best-effort; the json_import_state flag (already committed) is the real
	// idempotency guarantee, so a rename failure here is not fatal.
	if err := os.Rename(legacyPath, legacyPath+".migrated"); err != nil {
		log.Printf("warning: imported legacy data from %s but failed to rename it: %v", legacyPath, err)
	}
	return nil
}

// importVideoRow captures every field the videos/video_sources tables need, regardless
// of which legacy source (DownloadedVideo, FeedVideo, PrunedVideo, or a standalone
// Video) it came from.
type importVideoRow struct {
	sourceVideoID      string
	title              string
	publishDate        time.Time
	status             string // pending|downloaded|pruned
	downloadDate       time.Time
	addedAt            time.Time
	lastChecked        time.Time
	disablePruning     bool
	isShort            bool
	manualDownloadOnly bool
	url                string
	lastError          string
	lastErrorTime      time.Time
	uploaderName       string
	uploaderSourceID   string
}

func insertVideoRow(tx *sql.Tx, row importVideoRow) (int64, error) {
	res, err := tx.Exec(`INSERT INTO videos (
		title, publish_date, added_at, last_checked, disable_pruning, status, download_date,
		is_short, manual_download_only, last_error, last_error_time
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.title, timeToNull(row.publishDate), timeToNull(row.addedAt), timeToNull(row.lastChecked),
		row.disablePruning, row.status, timeToNull(row.downloadDate), row.isShort,
		row.manualDownloadOnly, row.lastError, timeToNull(row.lastErrorTime))
	if err != nil {
		return 0, err
	}
	videoPK, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	var url any
	if row.url != "" {
		url = row.url
	}
	if _, err := tx.Exec(`INSERT INTO video_sources (video_id, source_id, source_video_id, url, uploader_name, uploader_source_channel_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		videoPK, sourceYouTube, row.sourceVideoID, url, row.uploaderName, row.uploaderSourceID); err != nil {
		return 0, err
	}
	return videoPK, nil
}

func importChannel(tx *sql.Tx, ch Channel) error {
	res, err := tx.Exec(`INSERT INTO channels (
		name, last_checked, retention_days, disable_pruning, cutoff_date, video_quality,
		video_format, download_shorts, skip_auto_download, last_error, last_error_time,
		thumbnail_url, backlog_scan_complete
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.Name, timeToNull(ch.LastChecked), ch.RetentionDays, ch.DisablePruning,
		timeToNull(ch.CutoffDate), ch.VideoQuality, ch.VideoFormat, ch.DownloadShorts,
		ch.SkipAutoDownload, ch.LastError, timeToNull(ch.LastErrorTime), ch.ThumbnailURL,
		ch.BacklogScanComplete)
	if err != nil {
		return err
	}
	channelPK, err := res.LastInsertId()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`INSERT INTO channel_sources (channel_id, source_id, source_channel_id, url)
		VALUES (?, ?, ?, ?)`, channelPK, sourceYouTube, ch.ID, ch.URL); err != nil {
		return err
	}

	for _, dv := range ch.DownloadedVideos {
		if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
			sourceVideoID: dv.ID, title: dv.Title, publishDate: dv.PublishDate,
			status: "downloaded", downloadDate: dv.DownloadDate, disablePruning: dv.DisablePruning,
		}); err != nil {
			return fmt.Errorf("downloaded video %s: %w", dv.ID, err)
		}
	}
	for _, fv := range ch.FeedVideos {
		if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
			sourceVideoID: fv.ID, title: fv.Title, publishDate: fv.PublishedAt, status: "pending",
			addedAt: fv.AddedAt, isShort: fv.IsShort, manualDownloadOnly: fv.ManualDownloadOnly, url: fv.URL,
		}); err != nil {
			return fmt.Errorf("feed video %s: %w", fv.ID, err)
		}
	}
	for _, pv := range ch.PrunedVideos {
		if _, err := insertChannelVideo(tx, channelPK, importVideoRow{
			sourceVideoID: pv.ID, publishDate: pv.PublishDate, status: "pruned",
		}); err != nil {
			return fmt.Errorf("pruned video %s: %w", pv.ID, err)
		}
	}
	return nil
}

// insertChannelVideo inserts row as a new video and links it to channelPK via
// channel_videos, returning the new video's synthetic PK.
// insertChannelVideo inserts row as a new video and links it to channelPK via
// channel_videos, returning the new video's synthetic PK. If a video with the same
// source_video_id already exists (e.g. it's currently a standalone individually-tracked
// video being converted/merged into this channel), the existing row is relinked in place
// -- its individual_video_tracking ownership is dropped, its fields are updated to match
// the incoming data, and it's linked to the channel -- rather than attempting a duplicate
// insert (which would violate the video_sources uniqueness constraint) or silently
// skipping it (which would drop the video's download history during the move).
func insertChannelVideo(tx *sql.Tx, channelPK int64, row importVideoRow) (int64, error) {
	if row.sourceVideoID != "" {
		if existingPK, ok, err := resolveVideoPK(tx, row.sourceVideoID); err != nil {
			return 0, err
		} else if ok {
			if _, err := tx.Exec(`DELETE FROM individual_video_tracking WHERE video_id = ?`, existingPK); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(`UPDATE videos SET
				title = ?, publish_date = ?, added_at = ?, last_checked = ?, disable_pruning = ?,
				status = ?, download_date = ?, is_short = ?, manual_download_only = ?,
				last_error = ?, last_error_time = ?
				WHERE id = ?`,
				row.title, timeToNull(row.publishDate), timeToNull(row.addedAt), timeToNull(row.lastChecked),
				row.disablePruning, row.status, timeToNull(row.downloadDate), row.isShort, row.manualDownloadOnly,
				row.lastError, timeToNull(row.lastErrorTime), existingPK); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(`INSERT INTO channel_videos (channel_id, video_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
				channelPK, existingPK); err != nil {
				return 0, err
			}
			return existingPK, nil
		}
	}

	videoPK, err := insertVideoRow(tx, row)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO channel_videos (channel_id, video_id) VALUES (?, ?)`, channelPK, videoPK); err != nil {
		return 0, err
	}
	return videoPK, nil
}

// importStandaloneVideo imports a legacy individually-tracked Video. A standalone video
// only ever has zero or one DownloadedVideo entry in practice (it represents that one
// tracked video's own download record), and can never legitimately be 'pruned' in the
// legacy model: a standalone video that finished retention was deleted outright rather
// than remembered, so anything still present in legacy.Videos is either still pending
// (no DownloadedVideos entry) or currently downloaded (one entry).
func importStandaloneVideo(tx *sql.Tx, v Video) error {
	row := importVideoRow{
		sourceVideoID:    v.ID,
		title:            v.Title,
		status:           "pending",
		addedAt:          v.AddedDate,
		lastChecked:      v.LastChecked,
		disablePruning:   v.DisablePruning,
		url:              v.URL,
		lastError:        v.LastError,
		lastErrorTime:    v.LastErrorTime,
		uploaderName:     v.Uploader,
		uploaderSourceID: v.UploaderID,
	}
	if len(v.DownloadedVideos) > 0 {
		dv := v.DownloadedVideos[0]
		row.status = "downloaded"
		row.downloadDate = dv.DownloadDate
		row.publishDate = dv.PublishDate
		// The downloaded entry's own ID is the ground truth for what's actually on
		// disk and is what file-matching (ReconcileDownloadedVideos, CleanOldVideosForVideo)
		// keys on -- yt-dlp can resolve a slightly different canonical ID than the one
		// the video was originally tracked under, and MarkVideoAsDownloaded already
		// applies this same convention live (it overwrites source_video_id to match the
		// resolved download ID when they diverge), so import must match it for
		// consistency. Callers that reference a video by ID (convert-to-channel,
		// RemoveVideo, etc.) always do so using whatever GetVideos()/GetVideo() most
		// recently returned, which already reflects this canonical ID -- so this only
		// matters when the tracking ID and the resolved download ID genuinely differ.
		if dv.ID != "" {
			row.sourceVideoID = dv.ID
		}
		if dv.Title != "" {
			row.title = dv.Title
		}
	}

	videoPK, err := insertVideoRow(tx, row)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`INSERT INTO individual_video_tracking (video_id, retention_days, video_quality, video_format, download_shorts)
		VALUES (?, ?, ?, ?, ?)`, videoPK, v.RetentionDays, v.VideoQuality, v.VideoFormat, v.DownloadShorts)
	return err
}
