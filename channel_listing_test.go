package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ytdm/storage"
)

// fakeListingYtDlp is a fake yt-dlp: --flat-playlist prints the listing (exiting with
// listingExit), and a single-video lookup prints that video's metadata from videos, or
// fails like a members-only video. Every invocation is appended to a calls log.
type fakeListingYtDlp struct {
	t     *testing.T
	dir   string
	calls string
}

func newFakeListingYtDlp(t *testing.T, listing any, listingExit int, videos map[string]string) (*Downloader, *fakeListingYtDlp) {
	t.Helper()
	dir := t.TempDir()
	videoDir := filepath.Join(dir, "videos")
	if err := os.Mkdir(videoDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for id, uploadDate := range videos {
		meta, _ := json.Marshal(map[string]string{
			"id":          id,
			"title":       "Title " + id,
			"upload_date": uploadDate,
			"webpage_url": "https://www.youtube.com/watch?v=" + id,
		})
		if err := os.WriteFile(filepath.Join(videoDir, id+".json"), meta, 0644); err != nil {
			t.Fatalf("write video: %v", err)
		}
	}

	listingFile := filepath.Join(dir, "listing.json")
	listingJSON := []byte("")
	if listing != nil {
		listingJSON, _ = json.Marshal(listing)
	}
	if err := os.WriteFile(listingFile, listingJSON, 0644); err != nil {
		t.Fatalf("write listing: %v", err)
	}

	calls := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "fake-yt-dlp.sh")
	content := fmt.Sprintf(`#!/bin/sh
flat=""
for a in "$@"; do
  last="$a"
  [ "$a" = "--flat-playlist" ] && flat=1
done
echo "$*" >> %[1]q
if [ -n "$flat" ]; then
  cat %[2]q
  [ %[3]d -ne 0 ] && echo "ERROR: [youtube:tab] one tab failed" >&2
  exit %[3]d
fi
id="${last##*v=}"
if [ -f %[4]q/"$id".json ]; then
  cat %[4]q/"$id".json
  exit 0
fi
echo "ERROR: [youtube] $id: This video is available to this channel's members. Join this channel to get access to members-only content" >&2
exit 1
`, calls, listingFile, listingExit, videoDir)
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := DefaultConfig()
	cfg.YtDlp.Path = script
	return NewDownloader(cfg), &fakeListingYtDlp{t: t, dir: dir, calls: calls}
}

// extractedIDs lists the videos that were individually looked up, in call order.
func (f *fakeListingYtDlp) extractedIDs() []string {
	data, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatalf("read calls: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" || strings.Contains(line, "--flat-playlist") {
			continue
		}
		ids = append(ids, line[strings.LastIndex(line, "v=")+2:])
	}
	return ids
}

func (f *fakeListingYtDlp) listingCalls() int {
	data, _ := os.ReadFile(f.calls)
	return strings.Count(string(data), "--flat-playlist")
}

func video(id string) map[string]any {
	return map[string]any{"id": id, "title": "Listed " + id, "url": "https://www.youtube.com/watch?v=" + id}
}

func short(id string) map[string]any {
	return map[string]any{"id": id, "title": "Listed " + id, "url": "https://www.youtube.com/shorts/" + id}
}

func withField(entry map[string]any, key, value string) map[string]any {
	entry[key] = value
	return entry
}

func channelListing(tabs ...[]map[string]any) map[string]any {
	entries := make([]map[string]any, 0, len(tabs))
	for _, tab := range tabs {
		entries = append(entries, map[string]any{"_type": "playlist", "entries": tab})
	}
	return map[string]any{"_type": "playlist", "id": "UCchan", "entries": entries}
}

func ids(videos []VideoInfo) []string {
	out := make([]string, 0, len(videos))
	for _, v := range videos {
		out = append(out, v.ID)
	}
	return out
}

func newListingMemory() *listingMemory {
	return &listingMemory{outsideWindow: map[string]time.Time{}, unavailableUntil: map[string]time.Time{}}
}

func noneHandled(string) bool { return false }

var (
	listingNow   = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	listingSince = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func TestListingExtractsOnlyNewVideosAndStopsAtTheWindow(t *testing.T) {
	listing := channelListing([]map[string]any{
		withField(video("upcoming001"), "live_status", "is_upcoming"),
		video("newvideo001"),
		video("downloaded1"),
		video("pendingvid1"),
		withField(video("membersonly"), "availability", "subscriber_only"),
		video("newvideo002"),
		video("oldvideo001"),
		video("oldvideo002"),
	})
	d, fake := newFakeListingYtDlp(t, listing, 0, map[string]string{
		"newvideo001": "20261010",
		"newvideo002": "20261005",
		"oldvideo001": "20260920",
		"oldvideo002": "20260915",
	})
	pending := map[string]VideoInfo{
		"pendingvid1": {ID: "pendingvid1", Title: "Pending", PublishTime: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},
	}
	handled := func(id string) bool { return id == "downloaded1" }

	videos, stats, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, false, pending, handled)
	if err != nil {
		t.Fatalf("listing error = %v", err)
	}

	if got, want := ids(videos), []string{"newvideo001", "pendingvid1", "newvideo002"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("videos = %v, want %v", got, want)
	}
	if got, want := fake.extractedIDs(), []string{"newvideo001", "newvideo002", "oldvideo001"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("extracted = %v, want %v (no lookups for upcoming, downloaded, pending, members-only, or past the window)", got, want)
	}
	if !videos[0].PublishTime.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) || videos[0].Title != "Title newvideo001" {
		t.Fatalf("new video = %+v, want extracted date and title", videos[0])
	}
	if videos[1].Title != "Pending" {
		t.Fatalf("pending video = %+v, want stored info", videos[1])
	}
	if stats != (ChannelListingStats{Listed: 7, Extracted: 3, Unavailable: 1}) {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestListingWalksEachTabToItsOwnWindowEdge(t *testing.T) {
	listing := channelListing(
		[]map[string]any{video("videotab001"), video("videotabold")},
		[]map[string]any{video("livetab0001"), video("livetabold1")},
		[]map[string]any{short("shorttab001"), short("shorttabold")},
	)
	d, fake := newFakeListingYtDlp(t, listing, 0, map[string]string{
		"videotab001": "20261009",
		"videotabold": "20260901",
		"livetab0001": "20261003",
		"livetabold1": "20260801",
		"shorttab001": "20261002",
		"shorttabold": "20260701",
	})

	videos, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled)
	if err != nil {
		t.Fatalf("listing error = %v", err)
	}

	if got := strings.Join(ids(videos), ","); got != "videotab001,livetab0001,shorttab001" {
		t.Fatalf("videos = %s", got)
	}
	if !videos[2].IsShort || videos[0].IsShort {
		t.Fatalf("IsShort = %v/%v, want only the Shorts-tab video", videos[0].IsShort, videos[2].IsShort)
	}
	if got := len(fake.extractedIDs()); got != 6 {
		t.Fatalf("extractions = %d, want 6 (each tab stops after its first old video)", got)
	}
}

func TestListingSkipsShortsWithoutExtractingWhenShortsAreOff(t *testing.T) {
	listing := channelListing([]map[string]any{video("videotab001")}, []map[string]any{short("shorttab001")})
	d, fake := newFakeListingYtDlp(t, listing, 0, map[string]string{"videotab001": "20261009", "shorttab001": "20261009"})

	videos, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, false, nil, noneHandled)
	if err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if got := strings.Join(ids(videos), ","); got != "videotab001" {
		t.Fatalf("videos = %s", got)
	}
	if got := strings.Join(fake.extractedIDs(), ","); got != "videotab001" {
		t.Fatalf("extracted = %s, want no lookup for the short", got)
	}
}

func TestListingAcceptsASingleTabListing(t *testing.T) {
	listing := map[string]any{"_type": "playlist", "entries": []map[string]any{video("videotab001"), video("videotabold")}}
	d, _ := newFakeListingYtDlp(t, listing, 0, map[string]string{"videotab001": "20261009", "videotabold": "20260901"})

	videos, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/@chan/videos", listingSince, true, nil, noneHandled)
	if err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if got := strings.Join(ids(videos), ","); got != "videotab001" {
		t.Fatalf("videos = %s", got)
	}
}

func TestListingSkipsVideosThatFailToExtractAndRetriesThemLater(t *testing.T) {
	// "privatevid1" has no metadata, so the fake fails it like a members-only video.
	listing := channelListing([]map[string]any{video("privatevid1"), video("newvideo001"), video("oldvideo001")})
	d, fake := newFakeListingYtDlp(t, listing, 0, map[string]string{"newvideo001": "20261009", "oldvideo001": "20260901"})
	memory := newListingMemory()

	videos, stats, err := d.getChannelVideosFromListing(memory, listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled)
	if err != nil {
		t.Fatalf("one unavailable video must not fail the channel: %v", err)
	}
	if got := strings.Join(ids(videos), ","); got != "newvideo001" {
		t.Fatalf("videos = %s", got)
	}
	if stats.Unavailable != 1 {
		t.Fatalf("stats = %+v, want 1 unavailable", stats)
	}

	// The next check skips the failed video and doesn't re-extract the old one either.
	before := len(fake.extractedIDs())
	if _, _, err := d.getChannelVideosFromListing(memory, listingNow.Add(time.Hour), "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled); err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if got := fake.extractedIDs()[before:]; strings.Join(got, ",") != "newvideo001" {
		t.Fatalf("second check extracted %v, want only the still-untracked new video", got)
	}

	// After the retry window it's tried again.
	before = len(fake.extractedIDs())
	if _, _, err := d.getChannelVideosFromListing(memory, listingNow.Add(unavailableVideoRetryAfter+time.Hour), "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled); err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if got := fake.extractedIDs()[before:]; len(got) == 0 || got[0] != "privatevid1" {
		t.Fatalf("after the retry window extracted %v, want privatevid1 retried first", got)
	}
}

func TestListingTriesMembersOnlyVideosWhenCookiesAreConfigured(t *testing.T) {
	listing := channelListing([]map[string]any{withField(video("membersonly"), "availability", "subscriber_only"), video("oldvideo001")})
	d, fake := newFakeListingYtDlp(t, listing, 0, map[string]string{"membersonly": "20261009", "oldvideo001": "20260901"})
	d.config.YtDlp.CookiesFile = filepath.Join(fake.dir, "cookies.txt")

	videos, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled)
	if err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if got := strings.Join(ids(videos), ","); got != "membersonly" {
		t.Fatalf("videos = %s, want the members-only video (a member's cookies can unlock it)", got)
	}
}

func TestListingCapsExtractionsPerCheck(t *testing.T) {
	entries := make([]map[string]any, 0, channelListingMaxExtractions+5)
	videos := map[string]string{}
	for i := 0; i < channelListingMaxExtractions+5; i++ {
		id := fmt.Sprintf("backlog%04d", i)
		entries = append(entries, video(id))
		videos[id] = "20261009"
	}
	d, fake := newFakeListingYtDlp(t, channelListing(entries), 0, videos)

	found, stats, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled)
	if err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if len(found) != channelListingMaxExtractions || stats.Extracted != channelListingMaxExtractions || len(fake.extractedIDs()) != channelListingMaxExtractions {
		t.Fatalf("found %d, extracted %d (%d lookups), want %d", len(found), stats.Extracted, len(fake.extractedIDs()), channelListingMaxExtractions)
	}
}

func TestListingAcceptsPartialListingOutput(t *testing.T) {
	d, _ := newFakeListingYtDlp(t, channelListing([]map[string]any{video("newvideo001")}), 1, map[string]string{"newvideo001": "20261009"})

	videos, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled)
	if err != nil {
		t.Fatalf("a listing with output must be used even on a non-zero exit: %v", err)
	}
	if got := strings.Join(ids(videos), ","); got != "newvideo001" {
		t.Fatalf("videos = %s", got)
	}
}

func TestListingFailsWithoutAnyListing(t *testing.T) {
	d, _ := newFakeListingYtDlp(t, nil, 1, nil)

	if _, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled); err == nil {
		t.Fatal("want an error when yt-dlp produced no listing")
	}
}

func TestCookieRetrySkippedForPartialOutput(t *testing.T) {
	// The listing exits 1 with output and an auth-looking error; cookies are configured.
	d, fake := newFakeListingYtDlp(t, channelListing([]map[string]any{}), 1, nil)
	d.config.YtDlp.CookiesFile = filepath.Join(fake.dir, "cookies.txt")
	// Make the failing tab look like an auth error to isAuthError.
	script, _ := os.ReadFile(d.config.YtDlp.Path)
	patched := strings.Replace(string(script), "one tab failed", "Sign in to confirm you're not a bot", 1)
	if err := os.WriteFile(d.config.YtDlp.Path, []byte(patched), 0755); err != nil {
		t.Fatalf("patch script: %v", err)
	}

	if _, _, err := d.getChannelVideosFromListing(newListingMemory(), listingNow, "https://www.youtube.com/channel/UCchan", listingSince, true, nil, noneHandled); err != nil {
		t.Fatalf("listing error = %v", err)
	}
	if got := fake.listingCalls(); got != 1 {
		t.Fatalf("listing ran %d times, want 1 (no cookie retry after partial output)", got)
	}
}

func TestCookieRetryStillHappensWithoutOutput(t *testing.T) {
	// A single-video lookup that fails outright with an auth error is retried with cookies.
	d, fake := newFakeListingYtDlp(t, nil, 0, nil)
	d.config.YtDlp.CookiesFile = filepath.Join(fake.dir, "cookies.txt")

	if _, err := d.GetVideoInfo("https://www.youtube.com/watch?v=membersonly"); err == nil {
		t.Fatal("want an error for a video the fake can't serve")
	}
	data, _ := os.ReadFile(fake.calls)
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != 2 || !strings.Contains(calls[1], "--cookies") {
		t.Fatalf("calls = %q, want a cookie retry", calls)
	}
}

func TestChannelListingURL(t *testing.T) {
	cases := []struct {
		ch   storage.Channel
		want string
	}{
		{storage.Channel{ID: "UCabc", URL: "https://www.youtube.com/@abc/videos"}, "https://www.youtube.com/channel/UCabc"},
		{storage.Channel{ID: "legacy", URL: "https://www.youtube.com/channel/UCdef/videos"}, "https://www.youtube.com/channel/UCdef"},
		{storage.Channel{ID: "@handle", URL: "https://www.youtube.com/@handle"}, "https://www.youtube.com/@handle"},
	}
	for _, tc := range cases {
		if got := channelListingURL(tc.ch); got != tc.want {
			t.Errorf("channelListingURL(%+v) = %q, want %q", tc.ch, got, tc.want)
		}
	}
}

func TestPendingFeedVideos(t *testing.T) {
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	if err := store.AddChannel(storage.Channel{ID: "UCpending", Name: "Pending", URL: "https://www.youtube.com/channel/UCpending"}); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	published := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertFeedVideo("UCpending", storage.FeedVideo{ID: "pendingvid1", Title: "Pending", PublishedAt: published, IsShort: true, AddedAt: published}); err != nil {
		t.Fatalf("UpsertFeedVideo() error = %v", err)
	}

	pending := pendingFeedVideos(store, "UCpending")
	got := pending["pendingvid1"]
	if len(pending) != 1 || got.Title != "Pending" || !got.PublishTime.Equal(published) || !got.IsShort {
		t.Fatalf("pending = %+v", pending)
	}
	if len(pendingFeedVideos(store, "UCmissing")) != 0 {
		t.Fatal("want no pending videos for an unknown channel")
	}
}
