package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ytdm/storage"
)

type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// captureServer records every request it receives and answers with status.
func captureServer(t *testing.T, status int) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, capturedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), reqs...)
	}
}

// useTestNotifier swaps the process-wide notifier for one without retry delays.
func useTestNotifier(t *testing.T) *Notifier {
	t.Helper()
	prev := notifier
	n := NewNotifier()
	n.retryDelays = nil
	notifier = n
	t.Cleanup(func() {
		n.Wait()
		notifier = prev
	})
	return n
}

func configWithWebhooks(hooks ...WebhookConfig) *Config {
	cfg := DefaultConfig()
	cfg.Notifications.Webhooks = hooks
	return cfg
}

func decodeJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", b, err)
	}
	return m
}

func TestNtfyPublishesJSONToServerRootWithTopic(t *testing.T) {
	n := useTestNotifier(t)
	srv, got := captureServer(t, http.StatusOK)
	cfg := configWithWebhooks(WebhookConfig{
		Name:  "phone",
		URL:   srv.URL + "/ytdm-alerts",
		Token: "tk_secret",
		Template: MessageTemplate{
			Actions: []NotificationAction{
				{Action: "view", Label: "Open in app", URL: "vnd.youtube://{{.VideoID}}"},
				{Action: "http", Label: "Mark", URL: "{{.AppURL}}/hook?v={{.VideoID}}", Method: "POST", Clear: true},
				{Action: "view", Label: "Dropped", URL: "{{.Error}}"}, // renders empty: dropped
			},
		},
	})
	cfg.Notifications.AppURL = "https://ytdm.example"

	n.Notify(cfg, NotificationEvent{
		Event: EventVideoDownloaded, ChannelName: "Chan", ChannelThumbnailURL: "https://img/chan.jpg",
		VideoID: "abcdefghijk", Title: "A Video", ThumbnailURL: "https://i.ytimg.com/vi/abcdefghijk/maxresdefault.webp",
	}, "")
	n.Wait()

	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "" && r.Path != "/" {
		t.Errorf("expected POST to server root, got %s %q", r.Method, r.Path)
	}
	if auth := r.Header.Get("Authorization"); auth != "Bearer tk_secret" {
		t.Errorf("Authorization = %q", auth)
	}
	p := decodeJSON(t, r.Body)
	want := map[string]any{
		"topic":   "ytdm-alerts",
		"title":   "Chan",
		"message": "A Video",
		"click":   "https://www.youtube.com/watch?v=abcdefghijk",
		"icon":    "https://img/chan.jpg",
		// .webp is swapped for the always-present jpg so clients can preview it
		"attach":   "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg",
		"filename": "abcdefghijk.jpg",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
	actions, _ := p["actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions (empty URL dropped), got %v", p["actions"])
	}
	first := actions[0].(map[string]any)
	if first["url"] != "vnd.youtube://abcdefghijk" || first["label"] != "Open in app" {
		t.Errorf("first action = %v", first)
	}
	second := actions[1].(map[string]any)
	if second["url"] != "https://ytdm.example/hook?v=abcdefghijk" || second["method"] != "POST" || second["clear"] != true {
		t.Errorf("second action = %v", second)
	}
}

func TestNtfyUploadModeSendsThumbnailFileWithHeaders(t *testing.T) {
	n := useTestNotifier(t)
	srv, got := captureServer(t, http.StatusOK)
	thumb := filepath.Join(t.TempDir(), "S2026E1101 Video-abcdefghijk.jpg")
	if err := os.WriteFile(thumb, []byte("JPEGDATA"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := configWithWebhooks(WebhookConfig{
		URL: srv.URL + "/prefix/topic", Attachment: attachmentUpload, Username: "u", Password: "p",
		Template: MessageTemplate{Actions: []NotificationAction{{Label: "Wätch", URL: "{{.VideoURL}}"}}},
	})

	n.Notify(cfg, NotificationEvent{
		Event: EventVideoDownloaded, ChannelName: "Chännel", VideoID: "abcdefghijk",
		Title: "Line one", ThumbnailPath: thumb,
	}, "")
	n.Wait()

	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPut || r.Path != "/prefix/topic" {
		t.Errorf("expected PUT /prefix/topic, got %s %s", r.Method, r.Path)
	}
	if string(r.Body) != "JPEGDATA" {
		t.Errorf("body = %q, want the thumbnail bytes", r.Body)
	}
	dec := new(mime.WordDecoder)
	if title, _ := dec.DecodeHeader(r.Header.Get("X-Title")); title != "Chännel" {
		t.Errorf("X-Title decodes to %q", title)
	}
	if r.Header.Get("X-Filename") != filepath.Base(thumb) {
		t.Errorf("X-Filename = %q", r.Header.Get("X-Filename"))
	}
	actions := r.Header.Get("X-Actions")
	if strings.ContainsFunc(actions, func(c rune) bool { return c > 127 }) {
		t.Errorf("X-Actions must be ASCII, got %q", actions)
	}
	var parsed []NotificationAction
	if err := json.Unmarshal([]byte(actions), &parsed); err != nil || len(parsed) != 1 || parsed[0].Label != "Wätch" {
		t.Errorf("X-Actions = %q (%v)", actions, err)
	}
	if user, pass, ok := (&http.Request{Header: r.Header}).BasicAuth(); !ok || user != "u" || pass != "p" {
		t.Errorf("expected basic auth u/p")
	}
}

func TestJSONWebhookPayload(t *testing.T) {
	n := useTestNotifier(t)
	srv, got := captureServer(t, http.StatusNoContent)
	cfg := configWithWebhooks(WebhookConfig{
		Type: webhookTypeJSON, URL: srv.URL + "/hook", Headers: map[string]string{"X-Key": "k"},
	})

	n.Notify(cfg, NotificationEvent{Event: EventCookiesExpired, Scope: "system", Error: "Sign in to confirm you're not a bot"}, "")
	n.Wait()

	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Header.Get("X-Key") != "k" {
		t.Error("custom header missing")
	}
	p := decodeJSON(t, reqs[0].Body)
	if p["event"] != EventCookiesExpired || p["scope"] != "system" || p["rendered_title"] != "YouTube cookies need attention" {
		t.Errorf("payload = %v", p)
	}
}

func TestNotifyRespectsEventsDisabledAndCooldown(t *testing.T) {
	n := useTestNotifier(t)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return now }
	all, gotAll := captureServer(t, http.StatusOK)
	cookiesOnly, gotCookies := captureServer(t, http.StatusOK)
	off, gotOff := captureServer(t, http.StatusOK)
	cfg := configWithWebhooks(
		WebhookConfig{Type: webhookTypeJSON, URL: all.URL},
		WebhookConfig{Type: webhookTypeJSON, URL: cookiesOnly.URL, Events: []string{EventCookiesExpired}},
		WebhookConfig{Type: webhookTypeJSON, URL: off.URL, Disabled: true},
	)
	cfg.Notifications.RepeatCooldown = "1h"

	n.Notify(cfg, NotificationEvent{Event: EventVideoDownloaded, VideoID: "v1"}, "")
	n.Notify(cfg, NotificationEvent{Event: EventCookiesExpired}, EventCookiesExpired)
	n.Notify(cfg, NotificationEvent{Event: EventCookiesExpired}, EventCookiesExpired) // within cooldown
	now = now.Add(2 * time.Hour)
	n.Notify(cfg, NotificationEvent{Event: EventCookiesExpired}, EventCookiesExpired) // cooldown over
	n.Wait()

	if c := len(gotAll()); c != 3 {
		t.Errorf("subscribe-all webhook got %d requests, want 3", c)
	}
	if c := len(gotCookies()); c != 2 {
		t.Errorf("cookies-only webhook got %d requests, want 2", c)
	}
	if c := len(gotOff()); c != 0 {
		t.Errorf("disabled webhook got %d requests, want 0", c)
	}
}

func TestDeliveryRetriesServerErrorsButNotClientErrors(t *testing.T) {
	n := NewNotifier()
	n.retryDelays = []time.Duration{0, 0}
	failing, gotFailing := captureServer(t, http.StatusBadGateway)
	rejected, gotRejected := captureServer(t, http.StatusUnauthorized)

	if err := n.deliverWithRetry(WebhookConfig{Type: webhookTypeJSON, URL: failing.URL}, NotificationEvent{Event: EventTest}); err == nil {
		t.Error("expected an error from a 502 endpoint")
	}
	if err := n.deliverWithRetry(WebhookConfig{Type: webhookTypeJSON, URL: rejected.URL}, NotificationEvent{Event: EventTest}); err == nil {
		t.Error("expected an error from a 401 endpoint")
	}
	if c := len(gotFailing()); c != 3 {
		t.Errorf("502 endpoint got %d attempts, want 3", c)
	}
	if c := len(gotRejected()); c != 1 {
		t.Errorf("401 endpoint got %d attempts, want 1", c)
	}
}

func TestResolveMessageLayersEventOverWebhookOverBuiltin(t *testing.T) {
	w := WebhookConfig{
		Template: MessageTemplate{Title: "[ytdm] {{.ChannelName}}", Priority: 2, Tags: []string{"a"}},
		EventTemplates: map[string]MessageTemplate{
			EventVideoDownloaded: {Message: "New: {{.Title}}", Priority: 5},
		},
	}
	ev := NotificationEvent{Event: EventVideoDownloaded, ChannelName: "C", Title: "T", VideoURL: "https://v"}
	m := resolveMessage(w, ev)
	if m.Title != "[ytdm] C" || m.Message != "New: T" || m.Priority != 5 || m.Click != "https://v" || len(m.Tags) != 1 || m.Tags[0] != "a" {
		t.Errorf("resolved = %+v", m)
	}

	// Another event only gets the webhook-wide layer plus its own built-in defaults.
	m = resolveMessage(w, NotificationEvent{Event: EventChannelCheckFailed, ChannelName: "C", Error: "boom"})
	if m.Title != "[ytdm] C" || m.Message != "boom" || m.Priority != 2 {
		t.Errorf("resolved = %+v", m)
	}

	// Pending summary uses the count.
	m = resolveMessage(WebhookConfig{}, NotificationEvent{Event: EventVideoPending, ChannelName: "C", Count: 7})
	if m.Message != "7 new videos are waiting for a manual download" {
		t.Errorf("summary message = %q", m.Message)
	}
}

func TestChannelOptOutAndIndividualVideoSwitch(t *testing.T) {
	n := useTestNotifier(t)
	srv, got := captureServer(t, http.StatusOK)
	cfg := configWithWebhooks(WebhookConfig{Type: webhookTypeJSON, URL: srv.URL})

	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddChannel(storage.Channel{ID: "UCon", URL: "https://www.youtube.com/channel/UCon", Name: "On", ThumbnailURL: "https://img/on.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddChannel(storage.Channel{ID: "UCoff", URL: "u", Name: "Off", DisableNotifications: true}); err != nil {
		t.Fatal(err)
	}

	notifyChannelEvent(cfg, store, NotificationEvent{Event: EventVideoDownloaded, ChannelID: "UCon", VideoID: "v1"}, "")
	notifyChannelEvent(cfg, store, NotificationEvent{Event: EventVideoDownloaded, ChannelID: "UCoff", VideoID: "v2"}, "")
	notifyVideoEvent(cfg, NotificationEvent{Event: EventVideoDownloaded, VideoID: "v3"}, "")
	off := false
	cfg.Notifications.IndividualVideos = &off
	notifyVideoEvent(cfg, NotificationEvent{Event: EventVideoDownloaded, VideoID: "v4"}, "")
	n.Wait()

	var ids []string
	for _, r := range got() {
		p := decodeJSON(t, r.Body)
		ids = append(ids, p["video_id"].(string))
		if p["video_id"] == "v1" && (p["channel_url"] != "https://www.youtube.com/channel/UCon" || p["channel_thumbnail_url"] != "https://img/on.jpg") {
			t.Errorf("channel links not filled from storage: %v", p)
		}
	}
	if strings.Join(sortedStrings(ids), ",") != "v1,v3" {
		t.Errorf("delivered %v, want v1 and v3", ids)
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestNotifyNewPendingVideosSummarizesLargeBatches(t *testing.T) {
	n := useTestNotifier(t)
	srv, got := captureServer(t, http.StatusOK)
	cfg := configWithWebhooks(WebhookConfig{Type: webhookTypeJSON, URL: srv.URL})
	cfg.Notifications.PendingSummaryThreshold = 3
	ch := storage.Channel{ID: "UCx", Name: "X"}

	notifyNewPendingVideos(cfg, nil, ch, []VideoInfo{{ID: "a"}, {ID: "b"}})
	n.Wait()
	if c := len(got()); c != 2 {
		t.Fatalf("expected 2 individual notifications, got %d", c)
	}

	notifyNewPendingVideos(cfg, nil, ch, []VideoInfo{{ID: "c"}, {ID: "d"}, {ID: "e"}})
	n.Wait()
	reqs := got()
	if len(reqs) != 3 {
		t.Fatalf("expected one summary on top, got %d total", len(reqs))
	}
	if p := decodeJSON(t, reqs[2].Body); p["count"] != float64(3) || p["video_id"] != nil {
		t.Errorf("summary payload = %v", p)
	}
}

func TestSplitNtfyURL(t *testing.T) {
	tests := []struct{ in, base, topic string }{
		{"https://ntfy.sh/mytopic", "https://ntfy.sh", "mytopic"},
		{"https://example.com/ntfy/mytopic/", "https://example.com/ntfy", "mytopic"},
		{"http://10.0.0.2:8080/t?x=1", "http://10.0.0.2:8080", "t"},
	}
	for _, tt := range tests {
		base, topic, err := splitNtfyURL(tt.in)
		if err != nil || base != tt.base || topic != tt.topic {
			t.Errorf("splitNtfyURL(%q) = %q, %q, %v", tt.in, base, topic, err)
		}
	}
	if _, _, err := splitNtfyURL("https://ntfy.sh/"); err == nil {
		t.Error("expected an error for a URL with no topic")
	}
}

func TestValidateNotificationsConfig(t *testing.T) {
	ok := NotificationsConfig{RepeatCooldown: "6h", Webhooks: []WebhookConfig{
		{URL: "https://ntfy.sh/t", Events: []string{EventVideoDownloaded}},
		{Type: webhookTypeJSON, URL: "http://host/hook"},
	}}
	if err := validateNotificationsConfig(ok); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	bad := []NotificationsConfig{
		{RepeatCooldown: "soon"},
		{Webhooks: []WebhookConfig{{URL: "ftp://x/t"}}},
		{Webhooks: []WebhookConfig{{URL: "https://ntfy.sh"}}},
		{Webhooks: []WebhookConfig{{URL: "https://h/t", Type: "slack"}}},
		{Webhooks: []WebhookConfig{{URL: "https://h/t", Events: []string{"video.exploded"}}}},
		{Webhooks: []WebhookConfig{{URL: "https://h/t", Attachment: "inline"}}},
	}
	for i, c := range bad {
		if err := validateNotificationsConfig(c); err == nil {
			t.Errorf("bad config %d accepted", i)
		}
	}
}

func TestIsCookieProblem(t *testing.T) {
	yes := []string{
		"ERROR: [youtube] abc: Sign in to confirm you're not a bot. Use --cookies-from-browser or --cookies",
		"WARNING: The provided YouTube account cookies are no longer valid.",
	}
	no := []string{
		"ERROR: [youtube] abc: Join this channel to get access to members-only content",
		"ERROR: [youtube] abc: Private video",
		"ERROR: unable to download video data: HTTP Error 403: Forbidden",
	}
	for _, s := range yes {
		if !isCookieProblem(s) {
			t.Errorf("expected cookie problem: %q", s)
		}
	}
	for _, s := range no {
		if isCookieProblem(s) {
			t.Errorf("did not expect cookie problem: %q", s)
		}
	}
}

func TestConfigNotificationDefaultsAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"retention_days": 3}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !individualVideoNotificationsEnabled(cfg) || cfg.Notifications.RepeatCooldown != defaultRepeatCooldown ||
		cfg.Notifications.PendingSummaryThreshold != defaultPendingSummaryThreshold || cfg.Notifications.Webhooks == nil {
		t.Errorf("defaults not applied: %+v", cfg.Notifications)
	}

	snap := snapshotNotifications(cfg)
	if err := os.WriteFile(path, []byte(`{"notifications":{"individual_videos":false,"webhooks":[{"url":"https://ntfy.sh/a"}]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ReloadFromDisk(path); err != nil {
		t.Fatal(err)
	}
	if individualVideoNotificationsEnabled(cfg) || len(cfg.Notifications.Webhooks) != 1 {
		t.Errorf("reload not applied: %+v", cfg.Notifications)
	}
	if len(snap.Webhooks) != 0 {
		t.Error("an earlier snapshot must not change on reload")
	}
}

func TestNotificationAPI(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatal(err)
	}
	useTestNotifier(t)
	srv, got := captureServer(t, http.StatusOK)

	store, err := storage.NewStorage(filepath.Join("data", "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddChannel(storage.Channel{ID: "UCapi", URL: "u", Name: "Api"}); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	if err := cfg.Save("data/config.json"); err != nil {
		t.Fatal(err)
	}
	api := &APIServer{config: cfg, storage: store}

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.updateConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
		return rec
	}

	if rec := put(`{"notifications":{"webhooks":[{"url":"https://ntfy.sh"}]}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid webhook accepted: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(`{"notifications":{"individual_videos":false,"webhooks":[{"name":"n","type":"json","url":"` + srv.URL + `","events":["cookies.expired"]}]}}`); rec.Code != http.StatusOK {
		t.Fatalf("updateConfig = %d %s", rec.Code, rec.Body.String())
	}
	if individualVideoNotificationsEnabled(cfg) || len(cfg.Notifications.Webhooks) != 1 || cfg.Notifications.Webhooks[0].Events[0] != EventCookiesExpired {
		t.Errorf("notifications not saved: %+v", cfg.Notifications)
	}
	saved, _ := os.ReadFile("data/config.json")
	if !bytes.Contains(saved, []byte(`"cookies.expired"`)) {
		t.Error("notifications not persisted to config.json")
	}

	rec := httptest.NewRecorder()
	api.getConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if !strings.Contains(rec.Body.String(), `"notification_events"`) || !strings.Contains(rec.Body.String(), srv.URL) {
		t.Errorf("getConfig missing notifications: %s", rec.Body.String())
	}

	// Test endpoint: an unsubscribed event type still goes out, since tests are unfiltered.
	rec = httptest.NewRecorder()
	api.handleNotificationTest(rec, httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader(`{"index":0}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("test notification = %d %s", rec.Code, rec.Body.String())
	}
	if reqs := got(); len(reqs) != 1 || decodeJSON(t, reqs[0].Body)["event"] != EventTest {
		t.Errorf("expected one test delivery, got %d", len(reqs))
	}

	// Channel toggle: present field updates, absent field leaves it alone.
	channelPut := func(body string) {
		rec := httptest.NewRecorder()
		api.updateChannel(rec, httptest.NewRequest(http.MethodPut, "/api/channels/UCapi", strings.NewReader(body)), "UCapi")
		if rec.Code != http.StatusOK {
			t.Fatalf("updateChannel = %d %s", rec.Code, rec.Body.String())
		}
	}
	channelPut(`{"retention_days":5,"disable_notifications":true}`)
	if store.ChannelNotificationsEnabled("UCapi") {
		t.Error("disable_notifications=true not applied")
	}
	channelPut(`{"retention_days":6}`)
	if store.ChannelNotificationsEnabled("UCapi") {
		t.Error("omitting disable_notifications must not re-enable notifications")
	}
	channelPut(`{"retention_days":6,"disable_notifications":false}`)
	if !store.ChannelNotificationsEnabled("UCapi") {
		t.Error("disable_notifications=false not applied")
	}
}
