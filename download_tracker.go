package main

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DownloadPhase is where a tracked download currently is.
type DownloadPhase string

const (
	DownloadPhaseQueued      DownloadPhase = "queued"
	DownloadPhaseDownloading DownloadPhase = "downloading"
	DownloadPhaseProcessing  DownloadPhase = "processing"
)

// DownloadProgress is one progress update parsed from yt-dlp's output.
type DownloadProgress struct {
	Phase DownloadPhase
	// Stage names what is happening within the phase: "video", "audio", "subtitles"
	// while downloading, or a post-processing step such as "merging".
	Stage           string
	DownloadedBytes int64
	TotalBytes      int64   // 0 when unknown
	SpeedBps        float64 // 0 when unknown
	ETASeconds      int     // -1 when unknown
}

// ActiveDownload is a queued or running download as reported by GET /api/downloads.
type ActiveDownload struct {
	VideoID   string `json:"video_id"`
	Title     string `json:"title"`
	ScopeType string `json:"scope_type"` // "channel" or "video"
	ScopeID   string `json:"scope_id"`
	ScopeName string `json:"scope_name"`
	Manual    bool   `json:"manual"`

	Phase DownloadPhase `json:"phase"`
	Stage string        `json:"stage,omitempty"`
	// Percent of the current stage, nil when the size isn't known yet.
	Percent         *float64   `json:"percent"`
	DownloadedBytes int64      `json:"downloaded_bytes"`
	TotalBytes      int64      `json:"total_bytes"`
	SpeedBps        float64    `json:"speed_bps"`
	ETASeconds      int        `json:"eta_seconds"`
	QueuedAt        time.Time  `json:"queued_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
}

// downloadTracker records every download that is queued or running, keyed by YouTube
// video ID, so the UI can show the queue and live progress. It is purely informational:
// the download worker's own bookkeeping still decides what runs.
type downloadTracker struct {
	mu    sync.Mutex
	items map[string]*ActiveDownload
	now   func() time.Time
}

func newDownloadTracker() *downloadTracker {
	return &downloadTracker{items: map[string]*ActiveDownload{}, now: time.Now}
}

// downloads is the process-wide tracker shared by the download worker and the API's
// manual-download handlers.
var downloads = newDownloadTracker()

// Enqueue records a queued download. It returns false, changing nothing, when the
// video is already queued or downloading.
func (t *downloadTracker) Enqueue(d ActiveDownload) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d.VideoID == "" {
		return true
	}
	if _, exists := t.items[d.VideoID]; exists {
		return false
	}
	d.Phase = DownloadPhaseQueued
	d.QueuedAt = t.now()
	d.ETASeconds = -1
	t.items[d.VideoID] = &d
	return true
}

// Start marks a download as running, adding it first if it was never queued (manual
// downloads skip the queue). It returns false when the video is already running, so a
// second click can't start a duplicate yt-dlp process for the same video.
func (t *downloadTracker) Start(d ActiveDownload) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d.VideoID == "" {
		return true
	}
	now := t.now()
	existing, exists := t.items[d.VideoID]
	if exists && existing.Phase != DownloadPhaseQueued {
		return false
	}
	if !exists {
		d.QueuedAt = now
		existing = &d
		t.items[d.VideoID] = existing
	}
	existing.Phase = DownloadPhaseDownloading
	existing.StartedAt = &now
	existing.ETASeconds = -1
	return true
}

// Update applies a progress report to a running download.
func (t *downloadTracker) Update(videoID string, p DownloadProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.items[videoID]
	if !ok || d.Phase == DownloadPhaseQueued {
		return
	}
	d.Phase = p.Phase
	d.Stage = p.Stage
	if p.Phase == DownloadPhaseProcessing {
		// Post-processing has no byte counts; keep the last download numbers out of
		// the way so the UI shows an indeterminate state.
		d.Percent = nil
		d.SpeedBps = 0
		d.ETASeconds = -1
		return
	}
	d.DownloadedBytes = p.DownloadedBytes
	d.TotalBytes = p.TotalBytes
	d.SpeedBps = p.SpeedBps
	d.ETASeconds = p.ETASeconds
	d.Percent = nil
	if p.TotalBytes > 0 {
		pct := float64(p.DownloadedBytes) / float64(p.TotalBytes) * 100
		if pct > 100 {
			pct = 100
		}
		d.Percent = &pct
	}
}

// Finish removes a download once it has ended, whatever the outcome.
func (t *downloadTracker) Finish(videoID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.items, videoID)
}

// Has reports whether a video is queued or running.
func (t *downloadTracker) Has(videoID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.items[videoID]
	return ok
}

// Snapshot returns copies of every tracked download: running ones first (oldest start
// first), then the queue in the order it will run.
func (t *downloadTracker) Snapshot() []ActiveDownload {
	t.mu.Lock()
	out := make([]ActiveDownload, 0, len(t.items))
	for _, d := range t.items {
		c := *d
		if d.Percent != nil {
			pct := *d.Percent
			c.Percent = &pct
		}
		if d.StartedAt != nil {
			started := *d.StartedAt
			c.StartedAt = &started
		}
		out = append(out, c)
	}
	t.mu.Unlock()

	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].Phase != DownloadPhaseQueued, out[j].Phase != DownloadPhaseQueued
		if ai != aj {
			return ai
		}
		if ai && !out[i].StartedAt.Equal(*out[j].StartedAt) {
			return out[i].StartedAt.Before(*out[j].StartedAt)
		}
		if !out[i].QueuedAt.Equal(out[j].QueuedAt) {
			return out[i].QueuedAt.Before(out[j].QueuedAt)
		}
		return out[i].VideoID < out[j].VideoID
	})
	return out
}

// Prefixes ytdm gives its yt-dlp progress templates so those lines can be told apart
// from the rest of the output. Download progress goes to stdout and post-processing
// progress to stderr.
const (
	progressLinePrefix    = "@@ytdm-progress "
	postprocessLinePrefix = "@@ytdm-postprocess "
)

// progressTemplateArgs makes yt-dlp report progress as machine-readable lines, at most
// once a second, even though ytdm runs it with --quiet.
func progressTemplateArgs() []string {
	return []string{
		"--progress",
		"--newline",
		"--progress-delta", "1",
		"--progress-template", "download:" + progressLinePrefix +
			"%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s|%(info.vcodec)s|%(info.acodec)s",
		"--progress-template", "postprocess:" + postprocessLinePrefix +
			"%(progress.status)s|%(progress.postprocessor)s",
	}
}

// parseProgressLine parses one download progress line (without its prefix).
func parseProgressLine(line string) (DownloadProgress, bool) {
	fields := strings.Split(strings.TrimSpace(line), "|")
	if len(fields) != 8 {
		return DownloadProgress{}, false
	}
	p := DownloadProgress{Phase: DownloadPhaseDownloading, ETASeconds: -1}
	p.DownloadedBytes = int64(parseProgressNumber(fields[1]))
	p.TotalBytes = int64(parseProgressNumber(fields[2]))
	if p.TotalBytes <= 0 {
		p.TotalBytes = int64(parseProgressNumber(fields[3]))
	}
	p.SpeedBps = parseProgressNumber(fields[4])
	if eta := parseProgressNumber(fields[5]); eta >= 0 && fields[5] != "NA" {
		p.ETASeconds = int(eta)
	}
	if fields[0] == "finished" && p.TotalBytes <= 0 {
		p.TotalBytes = p.DownloadedBytes
	}
	p.Stage = progressStage(fields[6], fields[7])
	return p, true
}

// progressStage names the stream being downloaded from its codecs. Subtitles (and
// other non-media files) carry no codec information at all.
func progressStage(vcodec, acodec string) string {
	hasVideo := vcodec != "" && vcodec != "NA" && vcodec != "none"
	hasAudio := acodec != "" && acodec != "NA" && acodec != "none"
	switch {
	case hasVideo:
		return "video"
	case hasAudio:
		return "audio"
	case vcodec == "none" || acodec == "none":
		return "media"
	default:
		return "subtitles"
	}
}

// parsePostprocessLine parses one post-processing line (without its prefix).
func parsePostprocessLine(line string) (DownloadProgress, bool) {
	fields := strings.Split(strings.TrimSpace(line), "|")
	if len(fields) != 2 || fields[0] != "started" {
		return DownloadProgress{}, false
	}
	return DownloadProgress{Phase: DownloadPhaseProcessing, Stage: postprocessorLabel(fields[1]), ETASeconds: -1}, true
}

func postprocessorLabel(name string) string {
	switch name {
	case "Merger":
		return "merging"
	case "SponsorBlock":
		return "fetching SponsorBlock segments"
	case "ModifyChapters":
		return "marking chapters"
	case "Metadata", "FFmpegMetadata":
		return "adding metadata"
	case "EmbedThumbnail", "FFmpegThumbnailsConvertor", "ThumbnailsConvertor":
		return "processing thumbnail"
	case "FFmpegSubtitlesConvertor", "SubtitlesConvertor", "EmbedSubtitle":
		return "processing subtitles"
	case "MoveFiles":
		return "finishing"
	default:
		return strings.ToLower(name)
	}
}

func parseProgressNumber(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// lineSplitWriter passes yt-dlp output through to buf, except whole lines starting with
// prefix, which go to onLine (without the prefix) instead. That keeps progress lines
// out of the output the downloader inspects for errors and the printed id/title.
type lineSplitWriter struct {
	buf     interface{ Write([]byte) (int, error) }
	prefix  string
	onLine  func(string)
	partial []byte
}

func (w *lineSplitWriter) Write(p []byte) (int, error) {
	data := append(w.partial, p...)
	w.partial = nil
	for {
		idx := -1
		for i, b := range data {
			if b == '\n' || b == '\r' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		if data[idx] == '\r' {
			// "\r\n" is one line ending. A '\r' at the very end of this write may be
			// the first half of one, so wait for the next write to decide.
			if idx+1 == len(data) {
				break
			}
			if data[idx+1] == '\n' {
				idx++
			}
		}
		line := data[:idx+1]
		data = data[idx+1:]
		if rest, ok := strings.CutPrefix(string(line), w.prefix); ok {
			w.onLine(strings.TrimRight(rest, "\r\n"))
			continue
		}
		if _, err := w.buf.Write(line); err != nil {
			return 0, err
		}
	}
	if len(data) > 0 {
		w.partial = append([]byte(nil), data...)
	}
	return len(p), nil
}

// Flush writes out a trailing line that had no newline.
func (w *lineSplitWriter) Flush() {
	if len(w.partial) == 0 {
		return
	}
	line := string(w.partial)
	w.partial = nil
	if rest, ok := strings.CutPrefix(line, w.prefix); ok {
		w.onLine(rest)
		return
	}
	_, _ = w.buf.Write([]byte(line))
}
