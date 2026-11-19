package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// appStartedAt is when this process started, for the uptime shown in /api/status.
var appStartedAt = time.Now()

// schedulerHeartbeat holds the Unix time of the scheduler loop's last tick. /health
// reports the scheduler as stuck when it stops advancing.
var schedulerHeartbeat atomic.Int64

// schedulerStaleAfter is how long the scheduler loop (which ticks every 5s) may go
// without a heartbeat before /health fails.
const schedulerStaleAfter = 2 * time.Minute

func markSchedulerAlive() {
	schedulerHeartbeat.Store(time.Now().Unix())
}

// ytDlpVersionCache remembers `yt-dlp --version` so status polling doesn't start a
// yt-dlp process on every request. It is refreshed after the auto-updater runs, when
// the configured path changes, and at least hourly.
type ytDlpVersionCache struct {
	mu        sync.Mutex
	path      string
	version   string
	checkedAt time.Time
	lookup    func(path string) string
	now       func() time.Time
}

var ytDlpVersion = &ytDlpVersionCache{lookup: getYtDlpVersion, now: time.Now}

func (c *ytDlpVersionCache) get(path string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := time.Hour
	if c.version == "unknown" {
		// Retry a failed lookup sooner, e.g. while yt-dlp is being installed.
		ttl = time.Minute
	}
	if c.version == "" || c.path != path || c.now().Sub(c.checkedAt) > ttl {
		c.version = c.lookup(path)
		c.path = path
		c.checkedAt = c.now()
	}
	return c.version
}

// invalidate forces the next get to look the version up again.
func (c *ytDlpVersionCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version = ""
}

// healthCheck is one component's result in the /health response.
type healthCheck struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// healthReport is the /health response body.
type healthReport struct {
	Status        string                 `json:"status"` // "ok", "degraded" or "fail"
	UptimeSeconds int64                  `json:"uptime_seconds"`
	AppCommit     string                 `json:"app_commit"`
	Checks        map[string]healthCheck `json:"checks"`
	Warnings      []string               `json:"warnings,omitempty"`
}

// handleHealth reports whether ytdm can do its job: the database answers, the download
// directory is writable, yt-dlp is installed, and the scheduler loop is running. It
// returns 200 when healthy (status "ok", or "degraded" with warnings) and 503 when any
// check fails. It is cheap enough for a container health check every few seconds: it
// never runs yt-dlp (the version comes from the cache) and touches the disk once.
func (api *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		api.sendError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	report := healthReport{
		Status:        "ok",
		UptimeSeconds: int64(time.Since(appStartedAt).Seconds()),
		AppCommit:     getAppCommit(),
		Checks:        map[string]healthCheck{},
	}
	fail := func(name, detail string) {
		report.Checks[name] = healthCheck{OK: false, Detail: detail}
		report.Status = "fail"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := api.storage.Ping(ctx); err != nil {
		fail("database", err.Error())
	} else {
		report.Checks["database"] = healthCheck{OK: true, Detail: string(dbManager.get().Dialect)}
	}
	if warning := dbManager.get().Warning; warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}

	api.config.RLock()
	downloadDir := api.config.DownloadDir
	ytDlpPath := api.config.YtDlp.Path
	api.config.RUnlock()

	if err := checkDirWritable(downloadDir); err != nil {
		fail("download_dir", err.Error())
	} else {
		report.Checks["download_dir"] = healthCheck{OK: true, Detail: downloadDir}
	}

	if version := ytDlpVersion.get(ytDlpPath); version == "unknown" {
		fail("yt_dlp", fmt.Sprintf("could not run %s --version", ytDlpPath))
	} else {
		report.Checks["yt_dlp"] = healthCheck{OK: true, Detail: version}
	}

	if last := schedulerHeartbeat.Load(); last == 0 {
		// Still starting up: the scheduler hasn't completed its first tick.
		report.Checks["scheduler"] = healthCheck{OK: true, Detail: "starting"}
	} else if age := time.Since(time.Unix(last, 0)); age > schedulerStaleAfter {
		fail("scheduler", fmt.Sprintf("no heartbeat for %s", age.Round(time.Second)))
	} else {
		report.Checks["scheduler"] = healthCheck{OK: true}
	}

	if report.Status == "ok" && len(report.Warnings) > 0 {
		report.Status = "degraded"
	}

	code := http.StatusOK
	if report.Status == "fail" {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(report)
}

// checkDirWritable confirms dir exists and a file can be created in it.
func checkDirWritable(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	f, err := os.CreateTemp(dir, ".ytdm-health-*")
	if err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(filepath.Clean(name))
}
