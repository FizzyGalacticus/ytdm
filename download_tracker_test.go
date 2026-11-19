package main

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func newTestTracker() (*downloadTracker, *time.Time) {
	tr := newDownloadTracker()
	clock := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tr.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	return tr, &clock
}

func TestDownloadTrackerLifecycle(t *testing.T) {
	tr, _ := newTestTracker()

	if !tr.Enqueue(ActiveDownload{VideoID: "aaa", Title: "First"}) {
		t.Fatal("Enqueue(aaa) = false, want true")
	}
	if tr.Enqueue(ActiveDownload{VideoID: "aaa", Title: "Again"}) {
		t.Fatal("second Enqueue(aaa) = true, want false (already queued)")
	}
	if !tr.Enqueue(ActiveDownload{VideoID: "bbb", Title: "Second"}) {
		t.Fatal("Enqueue(bbb) = false")
	}

	snap := tr.Snapshot()
	if len(snap) != 2 || snap[0].VideoID != "aaa" || snap[1].VideoID != "bbb" {
		t.Fatalf("queue order = %+v, want aaa then bbb", snap)
	}
	for _, d := range snap {
		if d.Phase != DownloadPhaseQueued || d.StartedAt != nil || d.Percent != nil {
			t.Fatalf("queued item = %+v, want queued with no start time or percent", d)
		}
	}

	// Progress for a queued item is ignored: it hasn't started.
	tr.Update("bbb", DownloadProgress{Phase: DownloadPhaseDownloading, DownloadedBytes: 5, TotalBytes: 10})
	if got := tr.Snapshot()[1]; got.Phase != DownloadPhaseQueued {
		t.Fatalf("update applied to queued item: %+v", got)
	}

	if !tr.Start(ActiveDownload{VideoID: "bbb"}) {
		t.Fatal("Start(bbb) = false for a queued item")
	}
	if tr.Start(ActiveDownload{VideoID: "bbb"}) {
		t.Fatal("Start(bbb) twice = true, want false (already running)")
	}
	if tr.Enqueue(ActiveDownload{VideoID: "bbb"}) {
		t.Fatal("Enqueue of a running video = true, want false")
	}

	tr.Update("bbb", DownloadProgress{Phase: DownloadPhaseDownloading, Stage: "video", DownloadedBytes: 25, TotalBytes: 100, SpeedBps: 1000, ETASeconds: 3})
	snap = tr.Snapshot()
	if snap[0].VideoID != "bbb" {
		t.Fatalf("running item should sort first, got %+v", snap)
	}
	running := snap[0]
	if running.Phase != DownloadPhaseDownloading || running.Stage != "video" || running.Percent == nil || *running.Percent != 25 ||
		running.SpeedBps != 1000 || running.ETASeconds != 3 || running.StartedAt == nil || running.Title != "Second" {
		t.Fatalf("running item = %+v (percent %v)", running, running.Percent)
	}

	tr.Update("bbb", DownloadProgress{Phase: DownloadPhaseProcessing, Stage: "merging", ETASeconds: -1})
	if got := tr.Snapshot()[0]; got.Phase != DownloadPhaseProcessing || got.Stage != "merging" || got.Percent != nil || got.ETASeconds != -1 {
		t.Fatalf("processing item = %+v", got)
	}

	tr.Finish("bbb")
	if tr.Has("bbb") {
		t.Fatal("Has(bbb) after Finish = true")
	}
	if !tr.Has("aaa") {
		t.Fatal("Has(aaa) = false, want still queued")
	}
}

func TestDownloadTrackerStartWithoutQueueAndSnapshotIsACopy(t *testing.T) {
	tr, _ := newTestTracker()
	if !tr.Start(ActiveDownload{VideoID: "manual1", Title: "Manual", Manual: true}) {
		t.Fatal("Start of an untracked video = false")
	}
	tr.Update("manual1", DownloadProgress{Phase: DownloadPhaseDownloading, DownloadedBytes: 50, TotalBytes: 40})

	snap := tr.Snapshot()
	if len(snap) != 1 || snap[0].Phase != DownloadPhaseDownloading || !snap[0].Manual {
		t.Fatalf("snapshot = %+v", snap)
	}
	if *snap[0].Percent != 100 {
		t.Fatalf("percent = %v, want capped at 100", *snap[0].Percent)
	}

	*snap[0].Percent = 1
	if got := tr.Snapshot()[0]; *got.Percent != 100 {
		t.Fatal("modifying a snapshot changed the tracker")
	}

	// Unknown size leaves the percent unset instead of reporting 0%.
	tr.Update("manual1", DownloadProgress{Phase: DownloadPhaseDownloading, DownloadedBytes: 50})
	if got := tr.Snapshot()[0]; got.Percent != nil {
		t.Fatalf("percent with unknown size = %v, want nil", *got.Percent)
	}
}

func TestDownloadTrackerSortsRunningByStartThenQueueByEnqueue(t *testing.T) {
	tr, _ := newTestTracker()
	for _, id := range []string{"q1", "r1", "q2", "r2"} {
		tr.Enqueue(ActiveDownload{VideoID: id})
	}
	tr.Start(ActiveDownload{VideoID: "r2"})
	tr.Start(ActiveDownload{VideoID: "r1"})

	var got []string
	for _, d := range tr.Snapshot() {
		got = append(got, d.VideoID)
	}
	want := []string{"r2", "r1", "q1", "q2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestParseProgressLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want DownloadProgress
	}{
		{
			name: "video stream with total",
			line: "downloading|130048|223779|NA|9926313.38086659|0|avc1.4d401e|none",
			want: DownloadProgress{Phase: DownloadPhaseDownloading, Stage: "video", DownloadedBytes: 130048, TotalBytes: 223779, SpeedBps: 9926313.38086659, ETASeconds: 0},
		},
		{
			name: "audio stream with estimate only",
			line: "downloading|1024|NA|252182|NA|NA|none|opus",
			want: DownloadProgress{Phase: DownloadPhaseDownloading, Stage: "audio", DownloadedBytes: 1024, TotalBytes: 252182, ETASeconds: -1},
		},
		{
			name: "subtitle file finished without a size",
			line: "finished|440|NA|NA|5926.97|NA|NA|NA",
			want: DownloadProgress{Phase: DownloadPhaseDownloading, Stage: "subtitles", DownloadedBytes: 440, TotalBytes: 440, SpeedBps: 5926.97, ETASeconds: -1},
		},
		{
			name: "combined audio and video format",
			line: "downloading|10|100|NA|NA|7|avc1|mp4a",
			want: DownloadProgress{Phase: DownloadPhaseDownloading, Stage: "video", DownloadedBytes: 10, TotalBytes: 100, ETASeconds: 7},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseProgressLine(tt.line)
			if !ok || got != tt.want {
				t.Fatalf("parseProgressLine(%q) = %+v, %v; want %+v", tt.line, got, ok, tt.want)
			}
		})
	}

	if _, ok := parseProgressLine("not|enough|fields"); ok {
		t.Fatal("malformed line parsed")
	}
}

func TestParsePostprocessLine(t *testing.T) {
	got, ok := parsePostprocessLine("started|Merger")
	if !ok || got.Phase != DownloadPhaseProcessing || got.Stage != "merging" {
		t.Fatalf("parsePostprocessLine(started|Merger) = %+v, %v", got, ok)
	}
	if got, _ := parsePostprocessLine("started|SponsorBlock"); got.Stage != "fetching SponsorBlock segments" {
		t.Fatalf("SponsorBlock stage = %q", got.Stage)
	}
	if got, _ := parsePostprocessLine("started|SomethingNew"); got.Stage != "somethingnew" {
		t.Fatalf("unknown postprocessor stage = %q", got.Stage)
	}
	if _, ok := parsePostprocessLine("finished|Merger"); ok {
		t.Fatal("finished line should be ignored")
	}
}

func TestLineSplitWriterSeparatesProgressLines(t *testing.T) {
	var buf bytes.Buffer
	var lines []string
	w := &lineSplitWriter{buf: &buf, prefix: progressLinePrefix, onLine: func(l string) { lines = append(lines, l) }}

	// Writes split mid-line, mixed newline styles, and a final line with no newline.
	chunks := []string{
		"WARNING: something\n@@ytdm-pro",
		"gress downloading|1|2|NA|NA|NA|NA|NA\n",
		"@@ytdm-progress finished|2|2|NA|NA|NA|NA|NA\r",
		"\n",
		"abcdefghijk\tTitle with @@ytdm-progress inside",
	}
	for _, c := range chunks {
		if n, err := w.Write([]byte(c)); err != nil || n != len(c) {
			t.Fatalf("Write(%q) = %d, %v", c, n, err)
		}
	}
	w.Flush()

	if want := "WARNING: something\nabcdefghijk\tTitle with @@ytdm-progress inside"; buf.String() != want {
		t.Fatalf("passthrough = %q, want %q", buf.String(), want)
	}
	if len(lines) != 2 || lines[0] != "downloading|1|2|NA|NA|NA|NA|NA" || lines[1] != "finished|2|2|NA|NA|NA|NA|NA" {
		t.Fatalf("progress lines = %q", lines)
	}

	// The printed id/title line is still found by the existing parser.
	if id, title := parseDownloadedVideoInfo(buf.String()); id != "abcdefghijk" || title != "Title with @@ytdm-progress inside" {
		t.Fatalf("parseDownloadedVideoInfo = %q, %q", id, title)
	}
}

func TestDownloadWorkerTracksQueueAndSkipsVideosAlreadyDownloading(t *testing.T) {
	saved := downloads
	downloads = newDownloadTracker()
	t.Cleanup(func() { downloads = saved })

	cfg := newTestConfig(t)
	cfg.MaxConcurrent = 1

	release := make(chan struct{})
	started := make(chan string, 10)
	var mu sync.Mutex
	var executed []string
	execFn := func(ctx context.Context, req DownloadRequest) {
		mu.Lock()
		executed = append(executed, req.VideoID)
		mu.Unlock()
		started <- req.VideoID
		<-release
	}

	// A manual download of "busy" is already running when the worker gets it.
	downloads.Start(ActiveDownload{VideoID: "busy", Manual: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := make(chan DownloadRequest, 10)
	done := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(done)
	}()

	queue <- DownloadRequest{Kind: downloadKindChannel, VideoID: "one", VideoTitle: "One", ChannelID: "UC1", ChannelName: "Chan"}
	queue <- DownloadRequest{Kind: downloadKindChannel, VideoID: "busy", VideoTitle: "Busy"}
	queue <- DownloadRequest{Kind: downloadKindVideo, VideoID: "two", VideoTitle: "Two"}

	if id := <-started; id != "one" {
		t.Fatalf("first started = %q", id)
	}
	waitFor(t, func() bool { return len(downloads.Snapshot()) == 3 })

	snap := downloads.Snapshot()
	byID := map[string]ActiveDownload{}
	for _, d := range snap {
		byID[d.VideoID] = d
	}
	if d := byID["one"]; d.Phase != DownloadPhaseDownloading || d.ScopeType != "channel" || d.ScopeID != "UC1" || d.ScopeName != "Chan" || d.Title != "One" {
		t.Fatalf("one = %+v", d)
	}
	if d := byID["two"]; d.Phase != DownloadPhaseQueued || d.ScopeType != "video" {
		t.Fatalf("two = %+v", d)
	}
	if d := byID["busy"]; !d.Manual {
		t.Fatalf("busy should still be the manual download, got %+v", d)
	}

	release <- struct{}{}
	if id := <-started; id != "two" {
		t.Fatalf("second started = %q", id)
	}
	release <- struct{}{}
	waitFor(t, func() bool { return !downloads.Has("one") && !downloads.Has("two") })

	mu.Lock()
	defer mu.Unlock()
	for _, id := range executed {
		if id == "busy" {
			t.Fatal("worker started a second download of a video that was already downloading")
		}
	}
	cancel()
	<-done
}

func TestDownloadWorkerClearsQueuedEntriesOnShutdown(t *testing.T) {
	saved := downloads
	downloads = newDownloadTracker()
	t.Cleanup(func() { downloads = saved })

	cfg := newTestConfig(t)
	cfg.MaxConcurrent = 1
	block := make(chan struct{})
	execFn := func(ctx context.Context, req DownloadRequest) {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	queue := make(chan DownloadRequest, 10)
	done := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(done)
	}()
	queue <- DownloadRequest{VideoID: "running"}
	queue <- DownloadRequest{VideoID: "waiting"}
	waitFor(t, func() bool { return len(downloads.Snapshot()) == 2 })

	cancel()
	<-done
	if n := len(downloads.Snapshot()); n != 0 {
		t.Fatalf("%d entries left in the tracker after shutdown: %+v", n, downloads.Snapshot())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
