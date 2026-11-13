package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"ytdm/storage"
)

// Notification event names. Webhooks subscribe to any subset of these.
const (
	// EventVideoDownloaded fires when a new video finishes downloading and is available.
	EventVideoDownloaded = "video.downloaded"
	// EventVideoPending fires when a channel picks up a new feed entry that will not be
	// downloaded automatically (auto-download is off for the channel, or the video was
	// flagged manual-download-only because it is under two minutes).
	EventVideoPending = "video.pending"
	// EventDownloadFailed fires when a download attempt errors out.
	EventDownloadFailed = "download.failed"
	// EventCookiesExpired fires when yt-dlp fails in a way that suggests the YouTube
	// cookies are missing, expired, or need refreshing.
	EventCookiesExpired = "cookies.expired"
	// EventChannelCheckFailed fires when a channel's feed cannot be fetched.
	EventChannelCheckFailed = "channel.check_failed"
	// EventTest is sent by the "send test notification" API and is never filtered.
	EventTest = "test"
)

// NotificationEvents lists every subscribable event, in display order.
var NotificationEvents = []string{
	EventVideoDownloaded,
	EventVideoPending,
	EventDownloadFailed,
	EventCookiesExpired,
	EventChannelCheckFailed,
}

const (
	webhookTypeNtfy = "ntfy"
	webhookTypeJSON = "json"

	attachmentURL    = "url"
	attachmentUpload = "upload"
	attachmentNone   = "none"

	defaultRepeatCooldown          = "6h0m0s"
	defaultPendingSummaryThreshold = 5
)

// NotificationsConfig configures outgoing webhook notifications.
type NotificationsConfig struct {
	// IndividualVideos is the single switch for notifications about individually
	// tracked (non-channel) videos. Defaults to true.
	IndividualVideos *bool `json:"individual_videos"`
	// AppURL is the public URL of this ytdm instance, exposed to templates as
	// {{.AppURL}} (e.g. so a cookies alert can open the configuration page).
	AppURL string `json:"app_url"`
	// RepeatCooldown is a Go duration limiting how often the same failure alert
	// (cookies, a failing channel, a failing video) is repeated.
	RepeatCooldown string `json:"repeat_cooldown"`
	// PendingSummaryThreshold collapses a channel scan's new pending entries into one
	// summary notification once there are at least this many (e.g. a backlog).
	PendingSummaryThreshold int             `json:"pending_summary_threshold"`
	Webhooks                []WebhookConfig `json:"webhooks"`
}

// WebhookConfig is one notification destination.
type WebhookConfig struct {
	Name string `json:"name"`
	// Type is "ntfy" (default) or "json" (a generic JSON POST).
	Type string `json:"type"`
	// URL is the ntfy topic URL (e.g. https://ntfy.sh/my-topic) or the JSON endpoint.
	URL      string `json:"url"`
	Disabled bool   `json:"disabled"`
	// Events limits which events are sent. Empty means every event.
	Events   []string          `json:"events,omitempty"`
	Token    string            `json:"token,omitempty"` // sent as a Bearer token
	Username string            `json:"username,omitempty"`
	Password string            `json:"password,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	// Attachment controls the image on ntfy notifications, which is picked
	// automatically (the video's thumbnail, else the channel's picture; see
	// NotificationEvent.ImageURL): "url" (default) attaches it by URL, "upload" uploads
	// the downloaded thumbnail file when there is one, "none" sends no image.
	Attachment string `json:"attachment,omitempty"`
	// Template overrides the built-in message for every event; EventTemplates
	// overrides it further for individual events. Empty fields fall through.
	Template       MessageTemplate            `json:"template"`
	EventTemplates map[string]MessageTemplate `json:"event_templates,omitempty"`
}

// MessageTemplate holds Go text/template strings rendered against a NotificationEvent.
type MessageTemplate struct {
	Title    string               `json:"title,omitempty"`
	Message  string               `json:"message,omitempty"`
	Click    string               `json:"click,omitempty"`
	Icon     string               `json:"icon,omitempty"`
	Priority int                  `json:"priority,omitempty"` // ntfy 1 (min) to 5 (max)
	Tags     []string             `json:"tags,omitempty"`
	Markdown bool                 `json:"markdown,omitempty"`
	Actions  []NotificationAction `json:"actions,omitempty"`
}

// NotificationAction is an ntfy action button. URL, Body, and header values are
// templates.
type NotificationAction struct {
	Action  string            `json:"action"` // view, http, or broadcast
	Label   string            `json:"label"`
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Extras  map[string]string `json:"extras,omitempty"`
	Clear   bool              `json:"clear,omitempty"`
}

// NotificationEvent is the data behind one notification: the JSON webhook payload and
// the template data ({{.Title}}, {{.VideoURL}}, ...) for ntfy messages.
type NotificationEvent struct {
	Event               string    `json:"event"`
	Time                time.Time `json:"time"`
	Scope               string    `json:"scope"` // channel, video, or system
	ChannelID           string    `json:"channel_id,omitempty"`
	ChannelName         string    `json:"channel_name,omitempty"`
	ChannelURL          string    `json:"channel_url,omitempty"`
	ChannelThumbnailURL string    `json:"channel_thumbnail_url,omitempty"`
	VideoID             string    `json:"video_id,omitempty"`
	Title               string    `json:"title,omitempty"`
	VideoURL            string    `json:"video_url,omitempty"`
	ThumbnailURL        string    `json:"thumbnail_url,omitempty"`
	ThumbnailPath       string    `json:"-"`
	// ImageURL is the notification's picture, filled automatically: the video's
	// thumbnail, falling back to the channel's picture. Empty when neither applies
	// (e.g. a cookies alert).
	ImageURL    string    `json:"image_url,omitempty"`
	PublishDate time.Time `json:"publish_date,omitempty"`
	Error       string    `json:"error,omitempty"`
	Count       int       `json:"count,omitempty"` // >1 for a pending summary
	AppURL      string    `json:"app_url,omitempty"`
}

// builtinTemplates are the default ntfy messages per event.
var builtinTemplates = map[string]MessageTemplate{
	EventVideoDownloaded: {
		Title:    "{{.ChannelName}}",
		Message:  "{{.Title}}",
		Click:    "{{.VideoURL}}",
		Priority: 3,
		Tags:     []string{"tv"},
	},
	EventVideoPending: {
		Title:    "New video waiting: {{.ChannelName}}",
		Message:  "{{if gt .Count 1}}{{.Count}} new videos are waiting for a manual download{{else}}{{.Title}}{{end}}",
		Click:    "{{if gt .Count 1}}{{.AppURL}}{{else}}{{.VideoURL}}{{end}}",
		Priority: 3,
		Tags:     []string{"hourglass_flowing_sand"},
	},
	EventDownloadFailed: {
		Title:    "Download failed: {{if .ChannelName}}{{.ChannelName}}{{else}}{{.Title}}{{end}}",
		Message:  "{{.Title}}\n{{.Error}}",
		Click:    "{{.VideoURL}}",
		Priority: 4,
		Tags:     []string{"warning"},
	},
	EventCookiesExpired: {
		Title:    "YouTube cookies need attention",
		Message:  "yt-dlp hit an authentication error, so the YouTube cookies may be missing or expired.\n{{.Error}}",
		Click:    "{{.AppURL}}",
		Priority: 4,
		Tags:     []string{"cookie", "warning"},
	},
	EventChannelCheckFailed: {
		Title:    "Channel check failed: {{.ChannelName}}",
		Message:  "{{.Error}}",
		Click:    "{{.ChannelURL}}",
		Priority: 3,
		Tags:     []string{"warning"},
	},
	EventTest: {
		Title:    "ytdm test notification",
		Message:  "{{if .Title}}Sample video: {{.Title}}{{else}}Notifications are working.{{end}}",
		Click:    "{{if .VideoURL}}{{.VideoURL}}{{else}}{{.AppURL}}{{end}}",
		Priority: 3,
		Tags:     []string{"white_check_mark"},
	},
}

func defaultNotificationsConfig() NotificationsConfig {
	c := NotificationsConfig{}
	applyNotificationDefaults(&c)
	return c
}

func applyNotificationDefaults(c *NotificationsConfig) {
	if c.IndividualVideos == nil {
		enabled := true
		c.IndividualVideos = &enabled
	}
	if strings.TrimSpace(c.RepeatCooldown) == "" {
		c.RepeatCooldown = defaultRepeatCooldown
	}
	if c.PendingSummaryThreshold <= 0 {
		c.PendingSummaryThreshold = defaultPendingSummaryThreshold
	}
	if c.Webhooks == nil {
		c.Webhooks = []WebhookConfig{}
	}
}

// validateNotificationsConfig rejects settings that could never deliver.
func validateNotificationsConfig(c NotificationsConfig) error {
	if c.RepeatCooldown != "" {
		if _, err := time.ParseDuration(c.RepeatCooldown); err != nil {
			return fmt.Errorf("repeat_cooldown: %v", err)
		}
	}
	known := map[string]bool{}
	for _, e := range NotificationEvents {
		known[e] = true
	}
	for i, w := range c.Webhooks {
		label := w.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		u, err := url.Parse(strings.TrimSpace(w.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("webhook %s: url must be an http(s) URL", label)
		}
		switch w.Type {
		case "", webhookTypeNtfy:
			if _, _, err := splitNtfyURL(w.URL); err != nil {
				return fmt.Errorf("webhook %s: %v", label, err)
			}
		case webhookTypeJSON:
		default:
			return fmt.Errorf("webhook %s: unknown type %q (use ntfy or json)", label, w.Type)
		}
		switch w.Attachment {
		case "", attachmentURL, attachmentUpload, attachmentNone:
		default:
			return fmt.Errorf("webhook %s: unknown attachment mode %q", label, w.Attachment)
		}
		for _, e := range w.Events {
			if !known[e] {
				return fmt.Errorf("webhook %s: unknown event %q", label, e)
			}
		}
		for e := range w.EventTemplates {
			if !known[e] && e != EventTest {
				return fmt.Errorf("webhook %s: event_templates has unknown event %q", label, e)
			}
		}
	}
	return nil
}

func (w WebhookConfig) wants(event string) bool {
	if event == EventTest {
		return true
	}
	if len(w.Events) == 0 {
		return true
	}
	for _, e := range w.Events {
		if e == event {
			return true
		}
	}
	return false
}

func (w WebhookConfig) label() string {
	if w.Name != "" {
		return w.Name
	}
	return w.URL
}

// Notifier delivers notification events to the configured webhooks.
type Notifier struct {
	client *http.Client
	now    func() time.Time
	// retryDelays are the waits between delivery attempts.
	retryDelays []time.Duration

	mu       sync.Mutex
	lastSent map[string]time.Time
	wg       sync.WaitGroup
}

// notifier is the process-wide Notifier. Its only state is the repeat-alert cooldown.
var notifier = NewNotifier()

// NewNotifier returns a Notifier with production timeouts and retries.
func NewNotifier() *Notifier {
	return &Notifier{
		client:      &http.Client{Timeout: 30 * time.Second},
		now:         time.Now,
		retryDelays: []time.Duration{2 * time.Second, 10 * time.Second},
		lastSent:    map[string]time.Time{},
	}
}

// snapshotNotifications copies the notification settings under the config lock so
// deliveries never race a config reload.
func snapshotNotifications(cfg *Config) NotificationsConfig {
	if cfg == nil {
		return NotificationsConfig{}
	}
	cfg.RLock()
	defer cfg.RUnlock()
	n := cfg.Notifications
	n.Webhooks = append([]WebhookConfig(nil), cfg.Notifications.Webhooks...)
	return n
}

// Notify sends ev to every enabled webhook subscribed to its event, in the background.
// A non-empty cooldownKey suppresses repeats of the same alert within the configured
// repeat cooldown.
func (n *Notifier) Notify(cfg *Config, ev NotificationEvent, cooldownKey string) {
	nc := snapshotNotifications(cfg)
	var targets []WebhookConfig
	for _, w := range nc.Webhooks {
		if !w.Disabled && strings.TrimSpace(w.URL) != "" && w.wants(ev.Event) {
			targets = append(targets, w)
		}
	}
	if len(targets) == 0 {
		return
	}

	if cooldownKey != "" {
		cooldown, err := time.ParseDuration(nc.RepeatCooldown)
		if err != nil {
			cooldown, _ = time.ParseDuration(defaultRepeatCooldown)
		}
		now := n.now()
		n.mu.Lock()
		last, seen := n.lastSent[cooldownKey]
		if seen && now.Sub(last) < cooldown {
			n.mu.Unlock()
			return
		}
		n.lastSent[cooldownKey] = now
		n.mu.Unlock()
	}

	ev = n.prepare(nc, ev)
	for _, w := range targets {
		n.wg.Add(1)
		go func(w WebhookConfig) {
			defer n.wg.Done()
			if err := n.deliverWithRetry(w, ev); err != nil {
				logScopef("notifications", "webhooks", "Notifications", "Failed to send %s notification to %s: %v", ev.Event, w.label(), err)
			}
		}(w)
	}
}

// SendNow delivers ev to the given webhooks synchronously, without retries, filtering,
// or cooldown, and returns one error (or nil) per webhook. Used for test notifications.
func (n *Notifier) SendNow(nc NotificationsConfig, webhooks []WebhookConfig, ev NotificationEvent) []error {
	ev = n.prepare(nc, ev)
	errs := make([]error, len(webhooks))
	for i, w := range webhooks {
		errs[i] = n.deliver(w, ev)
	}
	return errs
}

// Wait blocks until all background deliveries finish (for tests and shutdown).
func (n *Notifier) Wait() {
	n.wg.Wait()
}

func (n *Notifier) prepare(nc NotificationsConfig, ev NotificationEvent) NotificationEvent {
	if ev.Time.IsZero() {
		ev.Time = n.now().UTC()
	}
	if ev.AppURL == "" {
		ev.AppURL = strings.TrimSpace(nc.AppURL)
	}
	if ev.VideoID != "" {
		if ev.VideoURL == "" {
			ev.VideoURL = normalizeChannelVideoURL(ev.VideoID)
		}
		if ev.ThumbnailURL == "" || !isImageURL(ev.ThumbnailURL) {
			ev.ThumbnailURL = "https://i.ytimg.com/vi/" + url.PathEscape(ev.VideoID) + "/hqdefault.jpg"
		}
	}
	if ev.ImageURL == "" {
		ev.ImageURL = ev.ThumbnailURL
	}
	if ev.ImageURL == "" {
		ev.ImageURL = ev.ChannelThumbnailURL
	}
	return ev
}

// isImageURL reports thumbnails ntfy clients can preview; YouTube's .webp variants are
// swapped for the always-present hqdefault.jpg.
func isImageURL(u string) bool {
	lower := strings.ToLower(u)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png")
}

func (n *Notifier) deliverWithRetry(w WebhookConfig, ev NotificationEvent) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = n.deliver(w, ev)
		if err == nil || !isRetryable(err) || attempt >= len(n.retryDelays) {
			return err
		}
		time.Sleep(n.retryDelays[attempt])
	}
}

type httpStatusError struct {
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("HTTP %d", e.status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

func isRetryable(err error) bool {
	if se, ok := err.(*httpStatusError); ok {
		return se.status == http.StatusTooManyRequests || se.status >= 500
	}
	return true // network errors and timeouts
}

func (n *Notifier) deliver(w WebhookConfig, ev NotificationEvent) error {
	switch w.Type {
	case webhookTypeJSON:
		return n.deliverJSON(w, ev)
	default:
		return n.deliverNtfy(w, ev)
	}
}

// resolvedMessage is a MessageTemplate after rendering.
type resolvedMessage struct {
	Title, Message, Click, Icon string
	Priority                    int
	Tags                        []string
	Markdown                    bool
	Actions                     []NotificationAction
}

// resolveMessage layers event template over webhook template over built-in default,
// field by field, then renders every template string against ev.
func resolveMessage(w WebhookConfig, ev NotificationEvent) resolvedMessage {
	layers := []MessageTemplate{w.EventTemplates[ev.Event], w.Template, builtinTemplates[ev.Event]}
	var t MessageTemplate
	for _, l := range layers {
		if t.Title == "" {
			t.Title = l.Title
		}
		if t.Message == "" {
			t.Message = l.Message
		}
		if t.Click == "" {
			t.Click = l.Click
		}
		if t.Icon == "" {
			t.Icon = l.Icon
		}
		if t.Priority == 0 {
			t.Priority = l.Priority
		}
		if t.Tags == nil {
			t.Tags = l.Tags
		}
		if t.Actions == nil {
			t.Actions = l.Actions
		}
		t.Markdown = t.Markdown || l.Markdown
	}

	r := resolvedMessage{
		Title:    renderTemplate(t.Title, ev),
		Message:  renderTemplate(t.Message, ev),
		Click:    renderTemplate(t.Click, ev),
		Icon:     renderTemplate(t.Icon, ev),
		Priority: t.Priority,
		Tags:     t.Tags,
		Markdown: t.Markdown,
	}
	if r.Message == "" {
		r.Message = ev.Event
	}
	if r.Icon == "" {
		r.Icon = ev.ChannelThumbnailURL // automatic: the channel's picture when known
	}
	for _, a := range t.Actions {
		ra := a
		ra.URL = renderTemplate(a.URL, ev)
		ra.Body = renderTemplate(a.Body, ev)
		if len(a.Headers) > 0 {
			ra.Headers = make(map[string]string, len(a.Headers))
			for k, v := range a.Headers {
				ra.Headers[k] = renderTemplate(v, ev)
			}
		}
		if len(a.Extras) > 0 {
			ra.Extras = make(map[string]string, len(a.Extras))
			for k, v := range a.Extras {
				ra.Extras[k] = renderTemplate(v, ev)
			}
		}
		if ra.Action == "" {
			ra.Action = "view"
		}
		// An action pointing nowhere (e.g. {{.VideoURL}} on a system event) is dropped
		// rather than sent as a broken button.
		if (ra.Action == "view" || ra.Action == "http") && strings.TrimSpace(ra.URL) == "" {
			continue
		}
		r.Actions = append(r.Actions, ra)
	}
	if len(r.Actions) > 3 {
		r.Actions = r.Actions[:3] // ntfy's limit
	}
	return r
}

func renderTemplate(text string, ev NotificationEvent) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	tmpl, err := template.New("n").Option("missingkey=zero").Parse(text)
	if err != nil {
		log.Printf("Notification template %q is invalid: %v", text, err)
		return text
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, ev); err != nil {
		log.Printf("Notification template %q failed: %v", text, err)
		return text
	}
	return strings.TrimSpace(b.String())
}

// splitNtfyURL splits https://host/prefix/topic into the server base and topic name.
func splitNtfyURL(raw string) (base, topic string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", err
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return "", "", fmt.Errorf("ntfy url must include the topic, e.g. https://ntfy.sh/my-topic")
	}
	idx := strings.LastIndex(path, "/")
	topic = path[idx+1:]
	u.Path = "/"
	if idx >= 0 {
		u.Path = "/" + path[:idx]
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/"), topic, nil
}

func (n *Notifier) deliverNtfy(w WebhookConfig, ev NotificationEvent) error {
	base, topic, err := splitNtfyURL(w.URL)
	if err != nil {
		return err
	}
	msg := resolveMessage(w, ev)

	mode := w.Attachment
	if mode == "" {
		mode = attachmentURL
	}
	if mode == attachmentUpload {
		if ev.ThumbnailPath != "" {
			if f, err := os.Open(ev.ThumbnailPath); err == nil {
				defer f.Close()
				return n.publishNtfyUpload(w, base+"/"+topic, msg, f, filepath.Base(ev.ThumbnailPath))
			}
		}
		mode = attachmentURL // no local file: attach the automatic image by URL instead
	}

	payload := map[string]any{
		"topic":   topic,
		"message": msg.Message,
	}
	if msg.Title != "" {
		payload["title"] = msg.Title
	}
	if msg.Priority > 0 {
		payload["priority"] = msg.Priority
	}
	if len(msg.Tags) > 0 {
		payload["tags"] = msg.Tags
	}
	if msg.Click != "" {
		payload["click"] = msg.Click
	}
	if msg.Icon != "" {
		payload["icon"] = msg.Icon
	}
	if msg.Markdown {
		payload["markdown"] = true
	}
	if len(msg.Actions) > 0 {
		payload["actions"] = msg.Actions
	}
	if mode == attachmentURL && ev.ImageURL != "" {
		payload["attach"] = ev.ImageURL
		// Channel pictures have no file extension; a .jpg name lets clients preview it.
		if ev.VideoID != "" && ev.ImageURL == ev.ThumbnailURL {
			payload["filename"] = ev.VideoID + ".jpg"
		} else {
			payload["filename"] = "channel.jpg"
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, base, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	applyWebhookAuth(req, w)
	return n.do(req)
}

// publishNtfyUpload publishes with the thumbnail file as the request body, which ntfy
// stores as an attachment; the message fields travel as headers.
func (n *Notifier) publishNtfyUpload(w WebhookConfig, topicURL string, msg resolvedMessage, file io.Reader, filename string) error {
	req, err := http.NewRequest(http.MethodPut, topicURL, file)
	if err != nil {
		return err
	}
	set := func(k, v string) {
		if v != "" {
			req.Header.Set(k, mime.BEncoding.Encode("UTF-8", v))
		}
	}
	set("X-Title", msg.Title)
	set("X-Message", msg.Message)
	set("X-Click", msg.Click)
	set("X-Icon", msg.Icon)
	set("X-Filename", filename)
	if msg.Priority > 0 {
		req.Header.Set("X-Priority", strconv.Itoa(msg.Priority))
	}
	if len(msg.Tags) > 0 {
		set("X-Tags", strings.Join(msg.Tags, ","))
	}
	if msg.Markdown {
		req.Header.Set("X-Markdown", "yes")
	}
	if len(msg.Actions) > 0 {
		actions, err := json.Marshal(msg.Actions)
		if err != nil {
			return err
		}
		req.Header.Set("X-Actions", asciiJSON(actions))
	}
	applyWebhookAuth(req, w)
	return n.do(req)
}

// jsonWebhookPayload is the body of a "json" webhook: the raw event plus the rendered
// title/message/click so simple receivers don't need their own formatting.
type jsonWebhookPayload struct {
	NotificationEvent
	RenderedTitle   string `json:"rendered_title,omitempty"`
	RenderedMessage string `json:"rendered_message,omitempty"`
	RenderedClick   string `json:"rendered_click,omitempty"`
}

func (n *Notifier) deliverJSON(w WebhookConfig, ev NotificationEvent) error {
	msg := resolveMessage(w, ev)
	body, err := json.Marshal(jsonWebhookPayload{
		NotificationEvent: ev,
		RenderedTitle:     msg.Title,
		RenderedMessage:   msg.Message,
		RenderedClick:     msg.Click,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSpace(w.URL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	applyWebhookAuth(req, w)
	return n.do(req)
}

func applyWebhookAuth(req *http.Request, w WebhookConfig) {
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	switch {
	case w.Token != "":
		req.Header.Set("Authorization", "Bearer "+w.Token)
	case w.Username != "" || w.Password != "":
		req.SetBasicAuth(w.Username, w.Password)
	}
}

func (n *Notifier) do(req *http.Request) error {
	req.Header.Set("User-Agent", "ytdm")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &httpStatusError{status: resp.StatusCode, body: strings.TrimSpace(string(snippet))}
}

// asciiJSON escapes non-ASCII runes so JSON can travel in an HTTP header.
func asciiJSON(b []byte) string {
	var sb strings.Builder
	for _, r := range string(b) {
		if r < 0x80 {
			sb.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&sb, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		fmt.Fprintf(&sb, `\u%04x`, r)
	}
	return sb.String()
}

// ---- Event helpers used by the scheduler, API, and downloader ----

// individualVideoNotificationsEnabled reports the global switch for individually
// tracked videos.
func individualVideoNotificationsEnabled(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	cfg.RLock()
	defer cfg.RUnlock()
	return cfg.Notifications.IndividualVideos == nil || *cfg.Notifications.IndividualVideos
}

// notifyChannelEvent sends a channel-scoped event unless the channel opted out.
func notifyChannelEvent(cfg *Config, store *storage.Storage, ev NotificationEvent, cooldownKey string) {
	if store != nil && !store.ChannelNotificationsEnabled(ev.ChannelID) {
		return
	}
	ev.Scope = "channel"
	if store != nil && (ev.ChannelURL == "" || ev.ChannelThumbnailURL == "") {
		if url, thumb, ok := store.ChannelLinks(ev.ChannelID); ok {
			if ev.ChannelURL == "" {
				ev.ChannelURL = url
			}
			if ev.ChannelThumbnailURL == "" {
				ev.ChannelThumbnailURL = thumb
			}
		}
	}
	notifier.Notify(cfg, ev, cooldownKey)
}

// notifyVideoEvent sends an event about an individually tracked video unless the
// global individual-video switch is off.
func notifyVideoEvent(cfg *Config, ev NotificationEvent, cooldownKey string) {
	if !individualVideoNotificationsEnabled(cfg) {
		return
	}
	ev.Scope = "video"
	notifier.Notify(cfg, ev, cooldownKey)
}

// notifyDownloadedFromResult fills a downloaded event from a DownloadResult.
func downloadedEvent(result *DownloadResult, videoID, title string, publish time.Time) NotificationEvent {
	ev := NotificationEvent{Event: EventVideoDownloaded, VideoID: videoID, Title: title, PublishDate: publish}
	if result != nil {
		if result.VideoID != "" {
			ev.VideoID = result.VideoID
		}
		if result.VideoTitle != "" {
			ev.Title = result.VideoTitle
		}
		ev.ThumbnailURL = result.ThumbnailURL
		ev.ThumbnailPath = result.ThumbnailPath
		ev.ChannelURL = result.ChannelURL
		ev.ChannelThumbnailURL = result.ChannelIcon
	}
	return ev
}

// cookieProblemIndicators are yt-dlp messages that point at the cookies themselves,
// narrower than isAuthError: members-only or private videos fail even with good cookies
// and must not raise a cookies alert.
var cookieProblemIndicators = []string{
	"not a bot",
	"cookies are no longer valid",
	"account cookies",
	"login required",
	"requires authentication",
	"http error 401",
	"use --cookies",
	"cookies-from-browser",
}

func isCookieProblem(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, indicator := range cookieProblemIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

// notifyIfCookieProblem raises a cookies alert (rate-limited by the repeat cooldown)
// when a failed yt-dlp run's output suggests the cookies need refreshing.
func notifyIfCookieProblem(cfg *Config, stderr string) {
	if !isCookieProblem(stderr) {
		return
	}
	notifier.Notify(cfg, NotificationEvent{
		Event: EventCookiesExpired,
		Scope: "system",
		Error: firstLines(strings.TrimSpace(stderr), 3, 400),
	}, EventCookiesExpired)
}

func firstLines(s string, maxLines, maxLen int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxLen {
		out = out[:maxLen] + "..."
	}
	return out
}

// sampleNotificationEvent builds a test event from the most recently downloaded video
// so a test shows a real thumbnail, click target, and action URLs.
func sampleNotificationEvent(store *storage.Storage) NotificationEvent {
	ev := NotificationEvent{Event: EventTest, Scope: "system"}
	if store == nil {
		return ev
	}
	var best storage.DownloadedVideo
	var bestChannel storage.Channel
	for _, ch := range store.GetChannels() {
		for _, dv := range ch.DownloadedVideos {
			if dv.DownloadDate.After(best.DownloadDate) {
				best, bestChannel = dv, ch
			}
		}
	}
	if best.ID == "" {
		return ev
	}
	ev.VideoID = best.ID
	ev.Title = best.Title
	ev.PublishDate = best.PublishDate
	ev.ChannelID = bestChannel.ID
	ev.ChannelName = bestChannel.Name
	ev.ChannelURL = bestChannel.URL
	ev.ChannelThumbnailURL = bestChannel.ThumbnailURL
	return ev
}
