// This file reassembles the normalized schema back into the JSON-compatible
// Channel/Video shapes the rest of the application (and static/app.js, via api.go's
// direct JSON serialization) expects. It is not the database's native shape.
package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

type channelVideoBuckets struct {
	downloaded []DownloadedVideo
	feed       []FeedVideo
	pruned     []PrunedVideo
}

// GetChannels returns all channels.
func (s *Storage) GetChannels() []Channel {
	channels, err := s.queryChannels("")
	if err != nil {
		return nil
	}
	return channels
}

// GetChannel returns the channel with the given canonical YouTube ID, if it exists.
func (s *Storage) GetChannel(id string) (Channel, bool) {
	channels, err := s.queryChannels(" AND cs.source_channel_id = ?", id)
	if err != nil || len(channels) == 0 {
		return Channel{}, false
	}
	return channels[0], true
}

func (s *Storage) queryChannels(whereClause string, args ...any) ([]Channel, error) {
	query := `SELECT c.id, cs.source_channel_id, cs.url, c.name, c.last_checked, c.retention_days,
		c.disable_pruning, c.cutoff_date, c.video_quality, c.video_format, c.download_shorts,
		c.skip_auto_download, c.last_error, c.last_error_time, c.thumbnail_url, c.backlog_scan_complete
		FROM channels c
		JOIN channel_sources cs ON cs.channel_id = c.id AND cs.source_id = ?` + whereClause + `
		ORDER BY c.id`
	fullArgs := append([]any{sourceYouTube}, args...)
	rows, err := s.db.Query(query, fullArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		pk int64
		ch Channel
	}
	var results []row
	for rows.Next() {
		var r row
		var lastChecked, cutoffDate, lastErrorTime sql.NullString
		if err := rows.Scan(&r.pk, &r.ch.ID, &r.ch.URL, &r.ch.Name, &lastChecked, &r.ch.RetentionDays,
			&r.ch.DisablePruning, &cutoffDate, &r.ch.VideoQuality, &r.ch.VideoFormat, &r.ch.DownloadShorts,
			&r.ch.SkipAutoDownload, &r.ch.LastError, &lastErrorTime, &r.ch.ThumbnailURL, &r.ch.BacklogScanComplete); err != nil {
			return nil, err
		}
		r.ch.LastChecked = nullToTime(lastChecked)
		r.ch.CutoffDate = nullToTime(cutoffDate)
		r.ch.LastErrorTime = nullToTime(lastErrorTime)
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}

	pks := make([]int64, len(results))
	for i, r := range results {
		pks[i] = r.pk
	}
	buckets, err := s.fetchChannelVideoBuckets(pks)
	if err != nil {
		return nil, err
	}

	channels := make([]Channel, len(results))
	for i, r := range results {
		b := buckets[r.pk]
		r.ch.DownloadedVideos = b.downloaded
		r.ch.FeedVideos = b.feed
		r.ch.PrunedVideos = b.pruned
		channels[i] = r.ch
	}
	return channels, nil
}

func (s *Storage) fetchChannelVideoBuckets(channelPKs []int64) (map[int64]channelVideoBuckets, error) {
	buckets := make(map[int64]channelVideoBuckets, len(channelPKs))
	if len(channelPKs) == 0 {
		return buckets, nil
	}

	placeholders := make([]string, len(channelPKs))
	args := make([]any, len(channelPKs)+1)
	args[0] = sourceYouTube
	for i, pk := range channelPKs {
		placeholders[i] = "?"
		args[i+1] = pk
	}
	query := fmt.Sprintf(`SELECT cv.channel_id, vs.source_video_id, v.title, v.publish_date,
		v.added_at, v.status, v.download_date, v.disable_pruning, v.is_short,
		v.manual_download_only, COALESCE(vs.url, 'https://www.youtube.com/watch?v=' || vs.source_video_id)
		FROM channel_videos cv
		JOIN videos v ON v.id = cv.video_id
		JOIN video_sources vs ON vs.video_id = v.id AND vs.source_id = ?
		WHERE cv.channel_id IN (%s)
		ORDER BY cv.channel_id, v.id`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var channelPK int64
		var sourceVideoID, title, status, url string
		var publishDate, addedAt, downloadDate sql.NullString
		var disablePruning, isShort, manualDownloadOnly bool
		if err := rows.Scan(&channelPK, &sourceVideoID, &title, &publishDate, &addedAt, &status,
			&downloadDate, &disablePruning, &isShort, &manualDownloadOnly, &url); err != nil {
			return nil, err
		}
		b := buckets[channelPK]
		switch status {
		case "downloaded":
			b.downloaded = append(b.downloaded, DownloadedVideo{
				ID: sourceVideoID, Title: title, DownloadDate: nullToTime(downloadDate),
				PublishDate: nullToTime(publishDate), DisablePruning: disablePruning,
			})
		case "pending":
			b.feed = append(b.feed, FeedVideo{
				ID: sourceVideoID, Title: title, URL: url,
				PublishedAt: nullToTime(publishDate), AddedAt: nullToTime(addedAt),
				IsShort: isShort, ManualDownloadOnly: manualDownloadOnly,
			})
		case "pruned":
			b.pruned = append(b.pruned, PrunedVideo{ID: sourceVideoID, PublishDate: nullToTime(publishDate)})
		}
		buckets[channelPK] = b
	}
	return buckets, rows.Err()
}

// GetVideos returns all standalone (individually-tracked) videos.
func (s *Storage) GetVideos() []Video {
	videos, err := s.queryVideos("")
	if err != nil {
		return nil
	}
	return videos
}

// GetVideo returns the standalone video with the given ID, if it exists.
func (s *Storage) GetVideo(id string) (Video, bool) {
	videos, err := s.queryVideos(" AND vs.source_video_id = ?", id)
	if err != nil || len(videos) == 0 {
		return Video{}, false
	}
	return videos[0], true
}

func (s *Storage) queryVideos(whereClause string, args ...any) ([]Video, error) {
	query := `SELECT vs.source_video_id,
		COALESCE(vs.url, 'https://www.youtube.com/watch?v=' || vs.source_video_id),
		v.title, v.added_at, v.last_checked, ivt.retention_days, v.disable_pruning,
		ivt.video_quality, ivt.video_format, ivt.download_shorts, vs.uploader_name,
		vs.uploader_source_channel_id, v.status, v.download_date, v.publish_date,
		v.last_error, v.last_error_time
		FROM videos v
		JOIN video_sources vs ON vs.video_id = v.id AND vs.source_id = ?
		JOIN individual_video_tracking ivt ON ivt.video_id = v.id` + whereClause + `
		ORDER BY v.id`
	fullArgs := append([]any{sourceYouTube}, args...)
	rows, err := s.db.Query(query, fullArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var videos []Video
	for rows.Next() {
		var v Video
		var status string
		var addedAt, lastChecked, downloadDate, publishDate, lastErrorTime sql.NullString
		if err := rows.Scan(&v.ID, &v.URL, &v.Title, &addedAt, &lastChecked, &v.RetentionDays,
			&v.DisablePruning, &v.VideoQuality, &v.VideoFormat, &v.DownloadShorts, &v.Uploader,
			&v.UploaderID, &status, &downloadDate, &publishDate, &v.LastError, &lastErrorTime); err != nil {
			return nil, err
		}
		v.AddedDate = nullToTime(addedAt)
		v.LastChecked = nullToTime(lastChecked)
		v.LastErrorTime = nullToTime(lastErrorTime)
		if status == "downloaded" {
			v.DownloadedVideos = []DownloadedVideo{{
				ID: v.ID, Title: v.Title, DownloadDate: nullToTime(downloadDate),
				PublishDate: nullToTime(publishDate), DisablePruning: v.DisablePruning,
			}}
		}
		videos = append(videos, v)
	}
	return videos, rows.Err()
}
