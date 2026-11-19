package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ytdm/storage"
)

func healthAPI(t *testing.T) (*APIServer, *storage.Storage) {
	t.Helper()
	store := newTestStorage(t)
	cfg := newTestConfig(t)
	cfg.YtDlp.Path = "fake-yt-dlp-for-health"

	savedCache := ytDlpVersion
	ytDlpVersion = &ytDlpVersionCache{lookup: func(string) string { return "2026.10.26" }, now: time.Now}
	savedBeat := schedulerHeartbeat.Load()
	markSchedulerAlive()
	t.Cleanup(func() {
		ytDlpVersion = savedCache
		schedulerHeartbeat.Store(savedBeat)
	})
	return &APIServer{config: cfg, storage: store}, store
}

func getHealth(t *testing.T, api *APIServer) (int, healthReport) {
	t.Helper()
	rec := httptest.NewRecorder()
	api.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var report healthReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode health body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, report
}

func TestHealthOK(t *testing.T) {
	api, _ := healthAPI(t)
	code, report := getHealth(t, api)
	if code != http.StatusOK || report.Status != "ok" {
		t.Fatalf("health = %d %+v", code, report)
	}
	for _, name := range []string{"database", "download_dir", "yt_dlp", "scheduler"} {
		if c, ok := report.Checks[name]; !ok || !c.OK {
			t.Fatalf("check %s = %+v", name, c)
		}
	}
	if report.Checks["yt_dlp"].Detail != "2026.10.26" {
		t.Fatalf("yt_dlp detail = %q", report.Checks["yt_dlp"].Detail)
	}
	// The writability probe cleans up after itself.
	entries, _ := os.ReadDir(api.config.DownloadDir)
	if len(entries) != 0 {
		t.Fatalf("health check left files in the download dir: %v", entries)
	}
}

func TestHealthFailsWhenDatabaseClosed(t *testing.T) {
	api, store := healthAPI(t)
	store.Close()
	code, report := getHealth(t, api)
	if code != http.StatusServiceUnavailable || report.Status != "fail" || report.Checks["database"].OK {
		t.Fatalf("health = %d %+v", code, report)
	}
}

func TestHealthFailsWhenDownloadDirMissingOrNotADir(t *testing.T) {
	api, _ := healthAPI(t)
	api.config.DownloadDir = filepath.Join(t.TempDir(), "missing")
	if code, report := getHealth(t, api); code != http.StatusServiceUnavailable || report.Checks["download_dir"].OK {
		t.Fatalf("missing dir: health = %d %+v", code, report)
	}

	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, []byte("x"), 0644)
	api.config.DownloadDir = file
	if code, report := getHealth(t, api); code != http.StatusServiceUnavailable || report.Checks["download_dir"].OK {
		t.Fatalf("file instead of dir: health = %d %+v", code, report)
	}
}

func TestHealthFailsWhenYtDlpCannotRun(t *testing.T) {
	api, _ := healthAPI(t)
	ytDlpVersion = &ytDlpVersionCache{lookup: func(string) string { return "unknown" }, now: time.Now}
	if code, report := getHealth(t, api); code != http.StatusServiceUnavailable || report.Checks["yt_dlp"].OK {
		t.Fatalf("health = %d %+v", code, report)
	}
}

func TestHealthSchedulerHeartbeat(t *testing.T) {
	api, _ := healthAPI(t)

	schedulerHeartbeat.Store(0)
	if code, report := getHealth(t, api); code != http.StatusOK || report.Checks["scheduler"].Detail != "starting" {
		t.Fatalf("before first tick: health = %d %+v", code, report)
	}

	schedulerHeartbeat.Store(time.Now().Add(-schedulerStaleAfter - time.Minute).Unix())
	if code, report := getHealth(t, api); code != http.StatusServiceUnavailable || report.Checks["scheduler"].OK {
		t.Fatalf("stale heartbeat: health = %d %+v", code, report)
	}
}

func TestHealthDegradedOnDatabaseWarning(t *testing.T) {
	api, _ := healthAPI(t)
	saved := dbManager.get()
	warned := saved
	warned.Warning = "PostgreSQL unreachable; running on fallback"
	dbManager.set(warned)
	t.Cleanup(func() { dbManager.set(saved) })

	code, report := getHealth(t, api)
	if code != http.StatusOK || report.Status != "degraded" || len(report.Warnings) != 1 {
		t.Fatalf("health = %d %+v", code, report)
	}
}

func TestHealthHeadAndMethods(t *testing.T) {
	api, _ := healthAPI(t)
	rec := httptest.NewRecorder()
	api.handleHealth(rec, httptest.NewRequest(http.MethodHead, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d, body %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	api.handleHealth(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec.Code)
	}
}

func TestYtDlpVersionCache(t *testing.T) {
	calls := 0
	clock := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	result := "2026.10.26"
	c := &ytDlpVersionCache{
		lookup: func(string) string { calls++; return result },
		now:    func() time.Time { return clock },
	}

	for i := 0; i < 5; i++ {
		if v := c.get("yt-dlp"); v != "2026.10.26" {
			t.Fatalf("get = %q", v)
		}
	}
	if calls != 1 {
		t.Fatalf("lookup ran %d times for repeated gets, want 1", calls)
	}

	c.get("/other/yt-dlp")
	if calls != 2 {
		t.Fatalf("path change didn't refresh (calls=%d)", calls)
	}

	result = "2026.11.01"
	c.invalidate()
	if v := c.get("/other/yt-dlp"); v != "2026.11.01" || calls != 3 {
		t.Fatalf("after invalidate get = %q (calls=%d)", v, calls)
	}

	clock = clock.Add(2 * time.Hour)
	c.get("/other/yt-dlp")
	if calls != 4 {
		t.Fatalf("hourly refresh didn't happen (calls=%d)", calls)
	}

	// A failed lookup is retried after a minute rather than an hour.
	result = "unknown"
	c.invalidate()
	c.get("/other/yt-dlp")
	clock = clock.Add(2 * time.Minute)
	result = "2026.11.01"
	if v := c.get("/other/yt-dlp"); v != "2026.11.01" {
		t.Fatalf("failed lookup not retried, get = %q", v)
	}
}

func TestStatusReportsUptimeAndCachedVersion(t *testing.T) {
	api, _ := healthAPI(t)
	rec := httptest.NewRecorder()
	api.handleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data["yt_dlp_version"] != "2026.10.26" {
		t.Fatalf("yt_dlp_version = %v", resp.Data["yt_dlp_version"])
	}
	started, err := time.Parse(time.RFC3339, resp.Data["started_at"].(string))
	if err != nil || !started.Equal(appStartedAt.UTC().Truncate(time.Second)) {
		t.Fatalf("started_at = %v (%v), want %v", resp.Data["started_at"], err, appStartedAt.UTC())
	}
	if secs, ok := resp.Data["uptime_seconds"].(float64); !ok || secs < 0 {
		t.Fatalf("uptime_seconds = %v", resp.Data["uptime_seconds"])
	}
	if _, err := time.ParseDuration(resp.Data["uptime"].(string)); err != nil {
		t.Fatalf("uptime %v is not a duration: %v", resp.Data["uptime"], err)
	}
}

func TestDownloadsEndpointListsTrackedDownloads(t *testing.T) {
	saved := downloads
	downloads = newDownloadTracker()
	t.Cleanup(func() { downloads = saved })
	downloads.Enqueue(ActiveDownload{VideoID: "abc", Title: "Queued one"})

	api, _ := healthAPI(t)
	rec := httptest.NewRecorder()
	api.handleDownloads(rec, httptest.NewRequest(http.MethodGet, "/api/downloads", nil))
	var resp struct {
		Success bool             `json:"success"`
		Data    []ActiveDownload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success || len(resp.Data) != 1 || resp.Data[0].VideoID != "abc" || resp.Data[0].Phase != DownloadPhaseQueued {
		t.Fatalf("downloads = %s", rec.Body.String())
	}

	// An empty queue is an empty list, not null, so the UI can iterate it directly.
	downloads.Finish("abc")
	rec = httptest.NewRecorder()
	api.handleDownloads(rec, httptest.NewRequest(http.MethodGet, "/api/downloads", nil))
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"data":[]`)) {
		t.Fatalf("empty downloads body = %s", rec.Body.String())
	}
}
