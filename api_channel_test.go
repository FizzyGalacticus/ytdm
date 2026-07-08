package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ytdm/storage"
)

func TestAddChannelResolvesCanonicalChannelID(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "data.db")
	store, err := storage.NewStorage(dataPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	script := filepath.Join(tmpDir, "fake-yt-dlp.sh")
	scriptContent := "#!/bin/sh\necho '{\"channel_id\":\"UCresolved123\"}'\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to create fake yt-dlp: %v", err)
	}

	cfg := DefaultConfig()
	cfg.YtDlp.Path = script

	api := &APIServer{config: cfg, storage: store}

	payload := map[string]interface{}{
		"url":  "https://www.youtube.com/@somehandle",
		"name": "Some Channel",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	api.addChannel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("addChannel status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if channels[0].ID != "UCresolved123" {
		t.Fatalf("stored channel ID = %q, want %q", channels[0].ID, "UCresolved123")
	}
}

func TestUpdateChannelDownloadedVideoPruning(t *testing.T) {
	tmpDir := t.TempDir()
	dataPath := filepath.Join(tmpDir, "data.db")
	store, err := storage.NewStorage(dataPath)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channel := storage.Channel{
		ID:   "UCprunetest",
		Name: "Prune Toggle",
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "vidABC", Title: "Tracked Video"},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	api := &APIServer{config: DefaultConfig(), storage: store}

	payload := map[string]bool{"disable_pruning": true}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/UCprunetest/videos/vidABC", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	api.handleChannelByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update downloaded video pruning status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := store.GetChannels()
	if len(channels) != 1 || len(channels[0].DownloadedVideos) != 1 {
		t.Fatalf("unexpected channels/downloaded_videos shape: %+v", channels)
	}
	if !channels[0].DownloadedVideos[0].DisablePruning {
		t.Fatalf("expected disable_pruning true on downloaded video")
	}
}

func TestConvertToChannelCreatesNew(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	// Add two individual videos from the same uploader. The downloaded-entry ID
	// matches the video's own tracked ID -- the common case where yt-dlp resolves the
	// same ID it was given (the divergent-ID case is covered separately in
	// downloader_test.go).
	for _, v := range []storage.Video{
		{ID: "vid-1", Title: "Video 1", Uploader: "Test Creator", UploaderID: "UCtest123",
			DownloadedVideos: []storage.DownloadedVideo{{ID: "vid-1", Title: "Video 1"}}},
		{ID: "vid-2", Title: "Video 2", Uploader: "Test Creator", UploaderID: "UCtest123",
			DownloadedVideos: []storage.DownloadedVideo{{ID: "vid-2", Title: "Video 2"}}},
	} {
		if err := store.AddVideo(v); err != nil {
			t.Fatalf("AddVideo() error = %v", err)
		}
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	api := &APIServer{config: cfg, storage: store}

	body, _ := json.Marshal(map[string]interface{}{
		"uploader_name":  "Test Creator",
		"uploader_id":    "UCtest123",
		"video_ids":      []string{"vid-1", "vid-2"},
		"video_quality":  "720",
		"retention_days": 14,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos/convert-to-channel", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleConvertToChannel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleConvertToChannel() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch := channels[0]
	if ch.ID != "UCtest123" {
		t.Errorf("channel ID = %q, want %q", ch.ID, "UCtest123")
	}
	if ch.Name != "Test Creator" {
		t.Errorf("channel Name = %q, want %q", ch.Name, "Test Creator")
	}
	if ch.VideoQuality != "720" {
		t.Errorf("VideoQuality = %q, want %q", ch.VideoQuality, "720")
	}
	if ch.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", ch.RetentionDays)
	}
	if len(ch.DownloadedVideos) != 2 {
		t.Errorf("DownloadedVideos count = %d, want 2", len(ch.DownloadedVideos))
	}

	// Individual video entries should be removed
	videos := store.GetVideos()
	if len(videos) != 0 {
		t.Errorf("expected 0 individual video entries, got %d", len(videos))
	}
}

func TestConvertToChannelCutoffDateUsesEarliestPublishDate(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	earliest := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2022, 6, 15, 0, 0, 0, 0, time.UTC)

	for _, v := range []storage.Video{
		{ID: "vid-1", Title: "Video 1", Uploader: "Test Creator", UploaderID: "UCtest123",
			DownloadedVideos: []storage.DownloadedVideo{{ID: "vid-1", Title: "Video 1", PublishDate: later}}},
		{ID: "vid-2", Title: "Video 2", Uploader: "Test Creator", UploaderID: "UCtest123",
			DownloadedVideos: []storage.DownloadedVideo{{ID: "vid-2", Title: "Video 2", PublishDate: earliest}}},
	} {
		if err := store.AddVideo(v); err != nil {
			t.Fatalf("AddVideo() error = %v", err)
		}
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	api := &APIServer{config: cfg, storage: store}

	body, _ := json.Marshal(map[string]interface{}{
		"uploader_name": "Test Creator",
		"uploader_id":   "UCtest123",
		"video_ids":     []string{"vid-1", "vid-2"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos/convert-to-channel", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleConvertToChannel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleConvertToChannel() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if ch := channels[0]; !ch.CutoffDate.Equal(earliest) {
		t.Errorf("CutoffDate = %v, want earliest published date %v", ch.CutoffDate, earliest)
	}
}

func TestConvertToChannelDisablePruningRequiresAllVideos(t *testing.T) {
	newStorageWithVideos := func(t *testing.T, videos []storage.Video) *storage.Storage {
		t.Helper()
		store, err := storage.NewStorage(filepath.Join(t.TempDir(), "data.db"))
		if err != nil {
			t.Fatalf("NewStorage() error = %v", err)
		}
		for _, v := range videos {
			if err := store.AddVideo(v); err != nil {
				t.Fatalf("AddVideo() error = %v", err)
			}
		}
		return store
	}

	convert := func(t *testing.T, store *storage.Storage) storage.Channel {
		t.Helper()
		cfg := DefaultConfig()
		cfg.DownloadDir = t.TempDir()
		api := &APIServer{config: cfg, storage: store}

		body, _ := json.Marshal(map[string]interface{}{
			"uploader_name": "Test Creator",
			"uploader_id":   "UCtest123",
			"video_ids":     []string{"vid-1", "vid-2"},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/videos/convert-to-channel", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		api.handleConvertToChannel(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("handleConvertToChannel() status = %d, body = %s", rec.Code, rec.Body.String())
		}
		channels := store.GetChannels()
		if len(channels) != 1 {
			t.Fatalf("expected 1 channel, got %d", len(channels))
		}
		return channels[0]
	}

	t.Run("mixed no-prune flags leave channel prunable", func(t *testing.T) {
		store := newStorageWithVideos(t, []storage.Video{
			{ID: "vid-1", Title: "Video 1", Uploader: "Test Creator", UploaderID: "UCtest123", DisablePruning: true},
			{ID: "vid-2", Title: "Video 2", Uploader: "Test Creator", UploaderID: "UCtest123", DisablePruning: false},
		})
		if ch := convert(t, store); ch.DisablePruning {
			t.Errorf("expected DisablePruning = false when not all videos are protected, got true")
		}
	})

	t.Run("all videos no-prune yields protected channel", func(t *testing.T) {
		store := newStorageWithVideos(t, []storage.Video{
			{ID: "vid-1", Title: "Video 1", Uploader: "Test Creator", UploaderID: "UCtest123", DisablePruning: true},
			{ID: "vid-2", Title: "Video 2", Uploader: "Test Creator", UploaderID: "UCtest123", DisablePruning: true},
		})
		if ch := convert(t, store); !ch.DisablePruning {
			t.Errorf("expected DisablePruning = true when all videos are protected, got false")
		}
	})

	t.Run("mixed group still protects the individually no-prune video's own record", func(t *testing.T) {
		// vid-1 was individually protected from pruning; vid-2 was not. Even though the
		// resulting channel is prunable overall (mixed group), vid-1's downloaded-video
		// record must carry its own protection forward so it is never pruned.
		// A standalone video's downloaded-record ID always matches its own tracked ID
		// (the canonical identity used throughout the app), so the converted channel's
		// DownloadedVideos entries carry the same IDs as the source videos.
		store := newStorageWithVideos(t, []storage.Video{
			{ID: "vid-1", Title: "Video 1", Uploader: "Test Creator", UploaderID: "UCtest123",
				DisablePruning:   true,
				DownloadedVideos: []storage.DownloadedVideo{{ID: "vid-1", Title: "Video 1"}}},
			{ID: "vid-2", Title: "Video 2", Uploader: "Test Creator", UploaderID: "UCtest123",
				DisablePruning:   false,
				DownloadedVideos: []storage.DownloadedVideo{{ID: "vid-2", Title: "Video 2"}}},
		})
		ch := convert(t, store)
		if ch.DisablePruning {
			t.Errorf("expected channel-level DisablePruning = false for a mixed group, got true")
		}
		var dv1, dv2 *storage.DownloadedVideo
		for i := range ch.DownloadedVideos {
			switch ch.DownloadedVideos[i].ID {
			case "vid-1":
				dv1 = &ch.DownloadedVideos[i]
			case "vid-2":
				dv2 = &ch.DownloadedVideos[i]
			}
		}
		if dv1 == nil || dv2 == nil {
			t.Fatalf("expected both vid-1 and vid-2 present, got %+v", ch.DownloadedVideos)
		}
		if !dv1.DisablePruning {
			t.Errorf("expected dv-1 (from a no-prune video) to carry DisablePruning = true on its own record")
		}
		if dv2.DisablePruning {
			t.Errorf("expected dv-2 (from a prunable video) to remain DisablePruning = false")
		}
	})
}

func TestConvertToChannelMergesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	// Pre-existing channel with one tracked download
	existingChannel := storage.Channel{
		ID:               "UCmergetest",
		Name:             "Merge Creator",
		URL:              "https://www.youtube.com/channel/UCmergetest",
		DownloadedVideos: []storage.DownloadedVideo{{ID: "existing-dv", Title: "Already There"}},
	}
	if err := store.AddChannel(existingChannel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	// Individual video entry with a new download
	if err := store.AddVideo(storage.Video{
		ID: "solo-vid", Title: "Solo", Uploader: "Merge Creator", UploaderID: "UCmergetest",
		DownloadedVideos: []storage.DownloadedVideo{{ID: "solo-vid", Title: "New Download"}},
	}); err != nil {
		t.Fatalf("AddVideo() error = %v", err)
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	api := &APIServer{config: cfg, storage: store}

	body, _ := json.Marshal(map[string]interface{}{
		"uploader_name": "Merge Creator",
		"uploader_id":   "UCmergetest",
		"video_ids":     []string{"solo-vid"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos/convert-to-channel", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleConvertToChannel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleConvertToChannel() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Still exactly one channel (no duplicate created)
	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	// Both downloads should be present
	if len(channels[0].DownloadedVideos) != 2 {
		t.Errorf("DownloadedVideos count = %d, want 2; got %+v", len(channels[0].DownloadedVideos), channels[0].DownloadedVideos)
	}

	// Individual video entry should be removed
	videos := store.GetVideos()
	if len(videos) != 0 {
		t.Errorf("expected 0 individual video entries, got %d", len(videos))
	}
}

func TestAddVideoRoutesUnderAlreadyTrackedChannel(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channelName := "Existing Channel"
	channel := storage.Channel{
		ID:           "UCtest456",
		Name:         channelName,
		URL:          "https://www.youtube.com/channel/UCtest456",
		VideoQuality: "720",
		VideoFormat:  "mp4",
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	channelDir := filepath.Join(tmpDir, sanitizeFilename(channelName))

	// Fake yt-dlp: responds to `--dump-json` (metadata lookup) with a fixed
	// upload date, and otherwise (download invocation) drops a dummy video
	// file into the channel dir and prints the id/title yt-dlp normally prints.
	script := filepath.Join(tmpDir, "fake-yt-dlp.sh")
	scriptContent := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--dump-json" ]; then
    echo '{"id":"vidXYZ","title":"Some Title","uploader":"%s","uploader_id":"UCtest456","channel_id":"UCtest456","upload_date":"20200101"}'
    exit 0
  fi
done
mkdir -p %q
echo "dummy" > %q
printf 'vidXYZ\tSome Title\n'
exit 0
`, channelName, channelDir, filepath.Join(channelDir, "vidXYZ.mp4"))
	if err := os.WriteFile(script, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to create fake yt-dlp: %v", err)
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	cfg.YtDlp.Path = script
	api := &APIServer{config: cfg, storage: store}

	body, _ := json.Marshal(map[string]interface{}{
		"url":         "https://www.youtube.com/watch?v=vidXYZ",
		"id":          "vidXYZ",
		"title":       "Some Title",
		"uploader":    channelName,
		"uploader_id": "UCtest456",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.addVideo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("addVideo() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// No standalone individual-video entry should be created.
	if videos := store.GetVideos(); len(videos) != 0 {
		t.Fatalf("expected 0 individual video entries, got %d: %+v", len(videos), videos)
	}

	// The actual download runs asynchronously; poll for it to land under the channel.
	deadline := time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		for _, ch := range store.GetChannels() {
			if ch.ID != "UCtest456" {
				continue
			}
			for _, dv := range ch.DownloadedVideos {
				if dv.ID == "vidXYZ" {
					found = true
				}
			}
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("expected video vidXYZ to be marked downloaded under channel UCtest456, channels = %+v", store.GetChannels())
	}
}

// TestAddVideoRoutesUnderTrackedChannelWithHandleUploaderID guards against the real-world
// bug where oEmbed/yt-dlp report the uploader as its @handle rather than the canonical
// UC... ID that tracked channels are keyed by. In that case the request itself only knows
// the handle-form UploaderID (as production traffic does), so addVideo must fall back to a
// dedicated yt-dlp lookup to resolve the canonical channel ID before it can find the match.
func TestAddVideoRoutesUnderTrackedChannelWithHandleUploaderID(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	channelName := "Existing Channel"
	channel := storage.Channel{
		ID:           "UCtest789",
		Name:         channelName,
		URL:          "https://www.youtube.com/@existingcreator",
		VideoQuality: "720",
		VideoFormat:  "mp4",
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	channelDir := filepath.Join(tmpDir, sanitizeFilename(channelName))

	// The fake yt-dlp reports the canonical channel_id distinctly from the @handle-form
	// uploader_id, mirroring real yt-dlp/oEmbed output for handle-based channels.
	script := filepath.Join(tmpDir, "fake-yt-dlp.sh")
	scriptContent := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--dump-json" ]; then
    echo '{"id":"vidABC","title":"Some Title","uploader":"%s","uploader_id":"@existingcreator","channel_id":"UCtest789","upload_date":"20210505"}'
    exit 0
  fi
done
mkdir -p %q
echo "dummy" > %q
printf 'vidABC\tSome Title\n'
exit 0
`, channelName, channelDir, filepath.Join(channelDir, "vidABC.mp4"))
	if err := os.WriteFile(script, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to create fake yt-dlp: %v", err)
	}

	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	cfg.YtDlp.Path = script
	api := &APIServer{config: cfg, storage: store}

	// Title is supplied so the primary oEmbed/yt-dlp metadata fetch is skipped (keeping the
	// test network-free), but uploader_id is deliberately the @handle form — exactly what
	// that metadata fetch would have produced in production — to force the canonical-ID
	// fallback lookup in resolveCanonicalChannelID.
	body, _ := json.Marshal(map[string]interface{}{
		"url":         "https://www.youtube.com/watch?v=vidABC",
		"id":          "vidABC",
		"title":       "Some Title",
		"uploader":    channelName,
		"uploader_id": "@existingcreator",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/videos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.addVideo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("addVideo() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// No standalone individual-video entry should be created.
	if videos := store.GetVideos(); len(videos) != 0 {
		t.Fatalf("expected 0 individual video entries, got %d: %+v", len(videos), videos)
	}

	deadline := time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		for _, ch := range store.GetChannels() {
			if ch.ID != "UCtest789" {
				continue
			}
			for _, dv := range ch.DownloadedVideos {
				if dv.ID == "vidABC" {
					found = true
				}
			}
		}
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("expected video vidABC to be marked downloaded under channel UCtest789, channels = %+v", store.GetChannels())
	}
}

func TestHandleDismissFeedVideo(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}

	publishedAt := time.Date(2023, 3, 4, 0, 0, 0, 0, time.UTC)
	channel := storage.Channel{
		ID:   "UCdismiss",
		Name: "Dismiss Channel",
		FeedVideos: []storage.FeedVideo{
			{ID: "fv-1", Title: "Pending Video", URL: "https://www.youtube.com/watch?v=fv-1", PublishedAt: publishedAt},
		},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	api := &APIServer{config: DefaultConfig(), storage: store}

	req := httptest.NewRequest(http.MethodPost, "/api/channels/UCdismiss/feed-videos/fv-1/dismiss", nil)
	rec := httptest.NewRecorder()
	api.handleDismissFeedVideo(rec, req, "UCdismiss", "fv-1")

	if rec.Code != http.StatusOK {
		t.Fatalf("handleDismissFeedVideo() status = %d, body = %s", rec.Code, rec.Body.String())
	}

	channels := store.GetChannels()
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch := channels[0]
	if len(ch.FeedVideos) != 0 {
		t.Errorf("expected feed video to be removed, got %+v", ch.FeedVideos)
	}
	if len(ch.PrunedVideos) != 1 || ch.PrunedVideos[0].ID != "fv-1" {
		t.Fatalf("expected fv-1 to be recorded as pruned, got %+v", ch.PrunedVideos)
	}
	if !ch.PrunedVideos[0].PublishDate.Equal(publishedAt) {
		t.Errorf("PrunedVideo.PublishDate = %v, want %v", ch.PrunedVideos[0].PublishDate, publishedAt)
	}

	// Dismissing again (already gone from FeedVideos) should 404, not duplicate the pruned entry.
	rec2 := httptest.NewRecorder()
	api.handleDismissFeedVideo(rec2, req, "UCdismiss", "fv-1")
	if rec2.Code != http.StatusNotFound {
		t.Errorf("expected 404 on re-dismiss of already-dismissed video, got %d", rec2.Code)
	}
}
