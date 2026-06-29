package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeChannelVideoURL(t *testing.T) {
	t.Run("raw id gets watch URL", func(t *testing.T) {
		got := normalizeChannelVideoURL("-0hOASBTWB4")
		want := "https://www.youtube.com/watch?v=-0hOASBTWB4"
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})

	t.Run("existing URL is preserved", func(t *testing.T) {
		input := "https://www.youtube.com/watch?v=-0hOASBTWB4"
		got := normalizeChannelVideoURL(input)
		if got != input {
			t.Fatalf("expected %q, got %q", input, got)
		}
	})
}

func TestExtractYouTubeVideoID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "raw id", input: "-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "watch url", input: "https://www.youtube.com/watch?v=-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "short url", input: "https://youtu.be/-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "shorts url", input: "https://www.youtube.com/shorts/-0hOASBTWB4", want: "-0hOASBTWB4"},
		{name: "unknown url", input: "https://example.com/watch?v=abc", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractYouTubeVideoID(tc.input)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// --- Download worker tests ---

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DownloadDir = t.TempDir()
	return cfg
}

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	s, err := NewStorage(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	return s
}

// TestDownloadWorkerDeduplication verifies that a VideoID already in-flight is not
// re-queued if the same request arrives while the first download is still running.
func TestDownloadWorkerDeduplication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount int32
	hold := make(chan struct{})

	execFn := func(_ context.Context, _ DownloadRequest) {
		atomic.AddInt32(&callCount, 1)
		<-hold
	}

	cfg := newTestConfig(t)
	cfg.MaxConcurrent = 3

	queue := make(chan DownloadRequest, 10)
	workerDone := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(workerDone)
	}()

	req := DownloadRequest{VideoID: "vid1", Kind: downloadKindChannel}

	// Send the first request and wait for the worker to start the download.
	queue <- req
	time.Sleep(50 * time.Millisecond)

	// Send the same video ID again while the first is still in-flight.
	queue <- req
	time.Sleep(50 * time.Millisecond)

	// Unblock downloads and shut down.
	close(hold)
	cancel()
	<-workerDone

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 download call (dedup), got %d", got)
	}
}

// TestDownloadWorkerMaxConcurrent verifies that the worker never exceeds
// config.MaxConcurrent parallel downloads.
func TestDownloadWorkerMaxConcurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const maxConcurrent = 2
	cfg := newTestConfig(t)
	cfg.MaxConcurrent = maxConcurrent

	var activeCount int32
	var peakCount int32
	started := make(chan struct{}, 10)
	hold := make(chan struct{})

	execFn := func(_ context.Context, _ DownloadRequest) {
		n := atomic.AddInt32(&activeCount, 1)
		for {
			peak := atomic.LoadInt32(&peakCount)
			if n <= peak || atomic.CompareAndSwapInt32(&peakCount, peak, n) {
				break
			}
		}
		started <- struct{}{}
		<-hold
		atomic.AddInt32(&activeCount, -1)
	}

	queue := make(chan DownloadRequest, 10)
	workerDone := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(workerDone)
	}()

	const numVideos = 5
	for i := 0; i < numVideos; i++ {
		queue <- DownloadRequest{VideoID: fmt.Sprintf("vid%d", i)}
	}

	// Wait for exactly maxConcurrent downloads to start.
	for i := 0; i < maxConcurrent; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for downloads to start")
		}
	}

	// A third download must NOT have started yet (they're all blocked on hold).
	select {
	case <-started:
		t.Errorf("more than %d downloads started simultaneously", maxConcurrent)
	case <-time.After(50 * time.Millisecond):
	}

	// Release downloads so the worker can finish.
	close(hold)
	cancel()
	<-workerDone

	if peak := atomic.LoadInt32(&peakCount); int(peak) > maxConcurrent {
		t.Errorf("peak concurrent downloads %d exceeded MaxConcurrent %d", peak, maxConcurrent)
	}
}

// TestDownloadWorkerDynamicMaxConcurrent verifies that a mid-run change to
// MaxConcurrent is respected for subsequently dispatched requests.
func TestDownloadWorkerDynamicMaxConcurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := newTestConfig(t)
	cfg.MaxConcurrent = 1 // start at 1

	var activeCount int32
	var peakCount int32
	hold := make(chan struct{})
	started := make(chan struct{}, 10)

	execFn := func(_ context.Context, _ DownloadRequest) {
		n := atomic.AddInt32(&activeCount, 1)
		for {
			peak := atomic.LoadInt32(&peakCount)
			if n <= peak || atomic.CompareAndSwapInt32(&peakCount, peak, n) {
				break
			}
		}
		started <- struct{}{}
		<-hold
		atomic.AddInt32(&activeCount, -1)
	}

	queue := make(chan DownloadRequest, 10)
	workerDone := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(workerDone)
	}()

	// Queue two downloads and confirm only one starts with MaxConcurrent=1.
	queue <- DownloadRequest{VideoID: "vidA"}
	queue <- DownloadRequest{VideoID: "vidB"}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first download never started")
	}
	select {
	case <-started:
		t.Error("second download started before MaxConcurrent was raised")
	case <-time.After(50 * time.Millisecond):
	}

	// Raise MaxConcurrent to 2 and unblock the first download so the second can start.
	cfg.Lock()
	cfg.MaxConcurrent = 2
	cfg.Unlock()

	// A completion event triggers tryFlush; release one slot.
	close(hold)

	select {
	case <-started:
		// second download finally started
	case <-time.After(2 * time.Second):
		t.Error("second download never started after MaxConcurrent was raised")
	}

	cancel()
	<-workerDone
}

// TestDownloadWorkerContextCancellation verifies the worker drains in-flight
// downloads and exits cleanly when the context is cancelled.
func TestDownloadWorkerContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var finished int32
	execFn := func(ctx context.Context, _ DownloadRequest) {
		// Simulate a download that respects context cancellation.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
		}
		atomic.AddInt32(&finished, 1)
	}

	cfg := newTestConfig(t)
	cfg.MaxConcurrent = 3

	queue := make(chan DownloadRequest, 10)
	workerDone := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(workerDone)
	}()

	queue <- DownloadRequest{VideoID: "v1"}
	queue <- DownloadRequest{VideoID: "v2"}
	time.Sleep(30 * time.Millisecond) // let both start

	cancel()

	select {
	case <-workerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("download worker did not stop after context cancellation")
	}
}

// TestDownloadWorkerPendingDrainedAfterInFlight verifies that requests added to
// the pending queue (beyond MaxConcurrent) are eventually dispatched once a
// running download completes.
func TestDownloadWorkerPendingDrainedAfterInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := newTestConfig(t)
	cfg.MaxConcurrent = 1

	var completed int32
	var mu sync.Mutex
	completedIDs := map[string]bool{}
	hold := make(chan struct{})
	first := make(chan struct{}, 1)

	execFn := func(_ context.Context, req DownloadRequest) {
		select {
		case first <- struct{}{}:
			<-hold // block first download
		default:
		}
		mu.Lock()
		completedIDs[req.VideoID] = true
		mu.Unlock()
		atomic.AddInt32(&completed, 1)
	}

	queue := make(chan DownloadRequest, 10)
	workerDone := make(chan struct{})
	go func() {
		runDownloadWorker(ctx, cfg, nil, nil, queue, execFn)
		close(workerDone)
	}()

	queue <- DownloadRequest{VideoID: "a"}
	queue <- DownloadRequest{VideoID: "b"}
	queue <- DownloadRequest{VideoID: "c"}

	// Wait for the first to start, then unblock it.
	<-first
	close(hold)

	// Wait for all three to complete.
	deadline := time.After(3 * time.Second)
	for {
		if atomic.LoadInt32(&completed) == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of 3 downloads completed", atomic.LoadInt32(&completed))
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-workerDone
}

// --- Monitor lifecycle tests ---

// TestRunChannelMonitorExitsWhenChannelMissing verifies the goroutine exits
// immediately when the channel ID is not present in storage.
func TestRunChannelMonitorExitsWhenChannelMissing(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	done := make(chan struct{})
	go func() {
		runChannelMonitor(context.Background(), "nonexistent-channel", cfg, s, downloader, queue)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("channel monitor did not exit for a missing channel")
	}
}

// TestRunVideoMonitorChecksAllVideos verifies that the single video monitor goroutine
// enqueues download requests for all pending standalone videos on startup.
func TestRunVideoMonitorChecksAllVideos(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	videos := []Video{
		{ID: "v1", URL: "https://www.youtube.com/watch?v=v1", Title: "Video 1"},
		{ID: "v2", URL: "https://www.youtube.com/watch?v=v2", Title: "Video 2"},
		{ID: "v3", URL: "https://www.youtube.com/watch?v=v3", Title: "Video 3"},
	}
	for _, v := range videos {
		if err := s.AddVideo(v); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wakeup := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		runVideoMonitor(ctx, cfg, s, downloader, queue, wakeup)
		close(done)
	}()

	// Collect queued requests (initial check runs immediately).
	queued := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(queued) < len(videos) {
		select {
		case req := <-queue:
			queued[req.VideoID] = true
		case <-deadline:
			t.Fatalf("timed out; only queued %d of %d videos: %v", len(queued), len(videos), queued)
		}
	}

	for _, v := range videos {
		if !queued[v.ID] {
			t.Errorf("video %s was not queued", v.ID)
		}
	}

	cancel()
	<-done
}

// TestRunVideoMonitorQueuesOnWakeup verifies that a video added after the monitor's
// initial check is queued when the wakeup channel is signalled — the same signal
// the manager sends when storage.AddVideo fires storage.NotifyCh().
func TestRunVideoMonitorQueuesOnWakeup(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)
	wakeup := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runVideoMonitor(ctx, cfg, s, downloader, queue, wakeup)
		close(done)
	}()

	// Let the initial check (no videos yet) complete.
	time.Sleep(50 * time.Millisecond)

	// Add a video after the monitor has already run its initial check.
	vid := Video{ID: "lateVid", URL: "https://www.youtube.com/watch?v=lateVid", Title: "Late Video"}
	if err := s.AddVideo(vid); err != nil {
		t.Fatal(err)
	}

	// Simulate what the manager does when it receives storage.NotifyCh().
	select {
	case wakeup <- struct{}{}:
	default:
	}

	// The monitor should wake up and queue the newly-added video.
	select {
	case req := <-queue:
		if req.VideoID != "lateVid" {
			t.Errorf("expected lateVid, got %s", req.VideoID)
		}
	case <-time.After(2 * time.Second):
		t.Error("video added after initial check was not queued on wakeup")
	}

	cancel()
	<-done
}

// TestRunChannelMonitorExitsOnContextCancel verifies the goroutine stops when
// its context is cancelled (no network calls are made because the channel
// doesn't exist).
func TestRunChannelMonitorExitsOnContextCancel(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runChannelMonitor(ctx, "nonexistent", cfg, s, downloader, queue)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("channel monitor did not exit after context cancellation")
	}
}

// TestRunVideoMonitorExitsOnContextCancel verifies the goroutine stops when
// its context is cancelled.
func TestRunVideoMonitorExitsOnContextCancel(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)
	wakeup := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runVideoMonitor(ctx, cfg, s, downloader, queue, wakeup)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("video monitor did not exit after context cancellation")
	}
}

// --- checkAndQueueChannel / checkAndQueueVideo unit tests ---

func TestCheckAndQueueChannelReturnsFalseWhenMissing(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	if checkAndQueueChannel(context.Background(), "nope", s, downloader, queue) {
		t.Error("expected false for missing channel")
	}
}

func TestCheckAndQueueVideoReturnsFalseWhenMissing(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	if checkAndQueueVideo(context.Background(), "nope", s, downloader, queue) {
		t.Error("expected false for missing video")
	}
}

// TestCheckAndQueueVideoAlreadyDownloaded verifies that a video with an
// existing DownloadedVideo entry is not re-queued.
func TestCheckAndQueueVideoAlreadyDownloaded(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	cfg.RetentionDays = 0 // disable pruning so cleanup is a no-op
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	vid := Video{
		ID:    "vidAlready",
		URL:   "https://www.youtube.com/watch?v=vidAlready",
		Title: "Already Downloaded",
		DownloadedVideos: []DownloadedVideo{
			{ID: "vidAlready", Title: "Already Downloaded", DownloadDate: time.Now()},
		},
	}
	if err := s.AddVideo(vid); err != nil {
		t.Fatal(err)
	}

	ok := checkAndQueueVideo(context.Background(), "vidAlready", s, downloader, queue)
	if !ok {
		t.Error("expected true (video still exists)")
	}

	select {
	case req := <-queue:
		t.Errorf("unexpected download request for already-downloaded video: %+v", req)
	default:
	}
}

// TestCheckAndQueueVideoQueuesWhenNotDownloaded verifies that a video without any
// DownloadedVideo entries is enqueued exactly once.
func TestCheckAndQueueVideoQueuesWhenNotDownloaded(t *testing.T) {
	s := newTestStorage(t)
	cfg := newTestConfig(t)
	downloader := NewDownloader(cfg)
	queue := make(chan DownloadRequest, 10)

	vid := Video{
		ID:    "vidNew",
		URL:   "https://www.youtube.com/watch?v=vidNew",
		Title: "New Video",
	}
	if err := s.AddVideo(vid); err != nil {
		t.Fatal(err)
	}
	// Drain the notify signal from AddVideo.
	select {
	case <-s.NotifyCh():
	default:
	}

	ok := checkAndQueueVideo(context.Background(), "vidNew", s, downloader, queue)
	if !ok {
		t.Error("expected true (video still exists)")
	}

	select {
	case req := <-queue:
		if req.VideoID != "vidNew" {
			t.Errorf("queued wrong video ID: got %q", req.VideoID)
		}
		if req.Kind != downloadKindVideo {
			t.Errorf("expected downloadKindVideo, got %v", req.Kind)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("expected a download request but got none")
	}
}

// --- Storage notify tests ---

func TestStorageGetChannel(t *testing.T) {
	s := newTestStorage(t)

	ch := Channel{ID: "UCtest", URL: "https://yt.com/@test", Name: "Test Channel"}
	if err := s.AddChannel(ch); err != nil {
		t.Fatal(err)
	}

	got, ok := s.GetChannel("UCtest")
	if !ok {
		t.Fatal("GetChannel returned false for existing channel")
	}
	if got.ID != "UCtest" || got.Name != "Test Channel" {
		t.Errorf("GetChannel returned unexpected channel: %+v", got)
	}

	_, ok = s.GetChannel("nonexistent")
	if ok {
		t.Error("GetChannel returned true for nonexistent channel")
	}
}

func TestStorageGetVideo(t *testing.T) {
	s := newTestStorage(t)

	vid := Video{ID: "vidTest", URL: "https://yt.com/watch?v=vidTest", Title: "Test Video"}
	if err := s.AddVideo(vid); err != nil {
		t.Fatal(err)
	}

	got, ok := s.GetVideo("vidTest")
	if !ok {
		t.Fatal("GetVideo returned false for existing video")
	}
	if got.ID != "vidTest" || got.Title != "Test Video" {
		t.Errorf("GetVideo returned unexpected video: %+v", got)
	}

	_, ok = s.GetVideo("nonexistent")
	if ok {
		t.Error("GetVideo returned true for nonexistent video")
	}
}

func TestStorageNotifyOnAddChannel(t *testing.T) {
	s := newTestStorage(t)
	// Drain any pre-existing signals.
	for len(s.NotifyCh()) > 0 {
		<-s.NotifyCh()
	}

	if err := s.AddChannel(Channel{ID: "UC1", URL: "u", Name: "c"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-s.NotifyCh():
	case <-time.After(200 * time.Millisecond):
		t.Error("expected notification after AddChannel")
	}
}

func TestStorageNotifyOnRemoveChannel(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddChannel(Channel{ID: "UC2", URL: "u", Name: "c"}); err != nil {
		t.Fatal(err)
	}
	// Drain add notification.
	select {
	case <-s.NotifyCh():
	default:
	}

	if err := s.RemoveChannel("UC2"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-s.NotifyCh():
	case <-time.After(200 * time.Millisecond):
		t.Error("expected notification after RemoveChannel")
	}
}

func TestStorageNotifyOnAddVideo(t *testing.T) {
	s := newTestStorage(t)
	for len(s.NotifyCh()) > 0 {
		<-s.NotifyCh()
	}

	if err := s.AddVideo(Video{ID: "v1", URL: "u", Title: "t"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-s.NotifyCh():
	case <-time.After(200 * time.Millisecond):
		t.Error("expected notification after AddVideo")
	}
}

func TestStorageNotifyOnRemoveVideo(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddVideo(Video{ID: "v2", URL: "u", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.NotifyCh():
	default:
	}

	if err := s.RemoveVideo("v2"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-s.NotifyCh():
	case <-time.After(200 * time.Millisecond):
		t.Error("expected notification after RemoveVideo")
	}
}
