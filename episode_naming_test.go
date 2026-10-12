package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"ytdm/storage"
)

// snlUpload is 2026-10-11 04:42:28 UTC, the upload time used throughout these tests.
var snlUpload = time.Date(2026, 10, 11, 4, 42, 28, 0, time.UTC)

func TestEpisodeNumbersUseTheUploadTime(t *testing.T) {
	season, episode, ok := episodeNumbers(&VideoMetadata{ID: "IuhG712NOak", UploadDate: "2026-10-11", Timestamp: snlUpload})
	if !ok || season != 2026 || episode != 1011044228 {
		t.Fatalf("episodeNumbers = (%d, %d, %v), want (2026, 1011044228, true)", season, episode, ok)
	}

	// Two uploads in the same minute (seen on real channels) still differ.
	_, sameMinute, _ := episodeNumbers(&VideoMetadata{ID: "other", UploadDate: "2026-10-11", Timestamp: snlUpload.Add(5 * time.Second)})
	if sameMinute == episode {
		t.Fatal("uploads seconds apart got the same episode number")
	}

	// The largest possible value fits a 32-bit episode number.
	_, latest, _ := episodeNumbers(&VideoMetadata{ID: "x", Timestamp: time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)})
	if latest != 1231235959 || latest > 1<<31-1 {
		t.Fatalf("latest episode number = %d", latest)
	}
}

func TestEpisodeNumbersWithoutAnUploadTime(t *testing.T) {
	first, firstEpisode, ok := episodeNumbers(&VideoMetadata{ID: "videoAAAAAA", UploadDate: "2026-06-03"})
	if !ok || first != 2026 || firstEpisode/1000000 != 603 {
		t.Fatalf("episodeNumbers = (%d, %d, %v), want season 2026 and an episode starting 0603", first, firstEpisode, ok)
	}

	_, again, _ := episodeNumbers(&VideoMetadata{ID: "videoAAAAAA", UploadDate: "2026-06-03"})
	_, other, _ := episodeNumbers(&VideoMetadata{ID: "videoBBBBBB", UploadDate: "2026-06-03"})
	if again != firstEpisode {
		t.Fatal("the fallback episode number must be stable for a video")
	}
	if other == firstEpisode {
		t.Fatal("same-day videos without upload times got the same episode number")
	}

	if _, _, ok := episodeNumbers(&VideoMetadata{ID: "x", UploadDate: "not a date"}); ok {
		t.Fatal("want ok=false without a usable upload date")
	}
}

func TestEpisodeVideoFilenameStem(t *testing.T) {
	cases := []struct {
		metadata VideoMetadata
		want     string
	}{
		{VideoMetadata{ID: "IuhG712NOak", Title: "Chess Tournament - SNL", Timestamp: snlUpload}, "S2026E1011044228 ChessTournamentSNL-IuhG712NOak"},
		{VideoMetadata{ID: "abc-123_XYZ", Title: "June: Vidéo #1!", Timestamp: time.Date(2026, 6, 3, 1, 2, 3, 0, time.UTC)}, "S2026E0603010203 JuneVido1-abc-123_XYZ"},
		{VideoMetadata{ID: "emojiOnly01", Title: "🎉🎉", Timestamp: snlUpload}, "S2026E1011044228 Video-emojiOnly01"},
	}
	for _, tc := range cases {
		got, ok := episodeVideoFilenameStem(&tc.metadata)
		if !ok || got != tc.want {
			t.Errorf("episodeVideoFilenameStem(%+v) = %q, %v; want %q", tc.metadata, got, ok, tc.want)
		}
	}
}

func TestParseEpisodeFilename(t *testing.T) {
	cases := []struct {
		name            string
		season, episode string
		ok              bool
	}{
		{"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4", "2026", "1011044228", true},
		{"S2026E0603010203 JuneVido1-abc.info.json", "2026", "603010203", true},
		{"2026-10-11 ChessTournamentSNL-IuhG712NOak.mp4", "", "", false},
		{"2026-10-11 ChessTournamentSNL-IuhG712NOak.info.nfo", "", "", false},
		{"S2026E10 short.mp4", "", "", false},
	}
	for _, tc := range cases {
		season, episode, ok := parseEpisodeFilename(tc.name)
		if season != tc.season || episode != tc.episode || ok != tc.ok {
			t.Errorf("parseEpisodeFilename(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.name, season, episode, ok, tc.season, tc.episode, tc.ok)
		}
	}
}

func TestNormalizeVideoFilenameLeavesEpisodeNamesAlone(t *testing.T) {
	for _, name := range []string{
		"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4",
		"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.info.json",
		"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.nfo",
	} {
		if got := normalizeVideoFilename(name, "IuhG712NOak"); got != name {
			t.Errorf("normalizeVideoFilename(%q) = %q, want it unchanged", name, got)
		}
	}
	// Legacy names keep their existing normalization.
	if got := normalizeVideoFilename("2026-10-11 Chess Tournament - SNL-IuhG712NOak.mp4", "IuhG712NOak"); got != "2026-10-11 ChessTournamentSNL-IuhG712NOak.mp4" {
		t.Errorf("legacy normalization = %q", got)
	}
}

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	names := make([]string, 0)
	for name := range listFileNames(dir) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestRenameNewDownloadFilesOnlyRenamesNewFiles(t *testing.T) {
	dir := t.TempDir()
	// Already on disk (and possibly already in Jellyfin): a legacy copy of the same video
	// and another video. Neither may be renamed.
	writeFiles(t, dir, "2026-10-11 ChessTournamentSNL-IuhG712NOak.mp4", "2026-10-11 ChessTournamentSNL-IuhG712NOak.info.nfo", "2026-10-11 CopTalkShowSNL-RbXnGCrYNKs.mp4")
	existing := listFileNames(dir)
	writeFiles(t, dir, "2026-10-11 Chess Tournament - SNL-IuhG712NOak.mp4", "2026-10-11 Chess Tournament - SNL-IuhG712NOak.info.json", "2026-10-11 Chess Tournament - SNL-IuhG712NOak.jpg")

	if err := renameNewDownloadFiles(dir, "IuhG712NOak", "S2026E1011044228 ChessTournamentSNL-IuhG712NOak", existing); err != nil {
		t.Fatalf("renameNewDownloadFiles() error = %v", err)
	}

	want := []string{
		"2026-10-11 ChessTournamentSNL-IuhG712NOak.info.nfo",
		"2026-10-11 ChessTournamentSNL-IuhG712NOak.mp4",
		"2026-10-11 CopTalkShowSNL-RbXnGCrYNKs.mp4",
		"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.info.json",
		"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.jpg",
		"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4",
	}
	if got := dirNames(t, dir); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("files = %q\nwant %q", got, want)
	}
}

func TestRenameNewDownloadFilesAvoidsOverwriting(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4")
	existing := listFileNames(dir)
	writeFiles(t, dir, "fresh-IuhG712NOak.mp4")

	if err := renameNewDownloadFiles(dir, "IuhG712NOak", "S2026E1011044228 ChessTournamentSNL-IuhG712NOak", existing); err != nil {
		t.Fatalf("renameNewDownloadFiles() error = %v", err)
	}

	want := []string{"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4", "S2026E1011044228 ChessTournamentSNL-IuhG712NOak_1.mp4"}
	if got := dirNames(t, dir); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("files = %q, want %q", got, want)
	}
}

// fakeDownloadYtDlp writes what a real download produces under a legacy-style name,
// in the directory of the -o template, and prints the id/title line.
func fakeDownloadYtDlp(t *testing.T, info map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	infoJSON, _ := json.Marshal(info)
	infoPath := filepath.Join(dir, "info.json")
	if err := os.WriteFile(infoPath, infoJSON, 0644); err != nil {
		t.Fatalf("write info: %v", err)
	}
	script := filepath.Join(dir, "fake-yt-dlp.sh")
	content := fmt.Sprintf(`#!/bin/sh
prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
dir=$(dirname "$out")
mkdir -p "$dir"
base="$dir/2026-10-11 %[1]s-%[2]s"
printf 'video' > "$base.mp4"
cp %[3]q "$base.info.json"
printf 'jpg' > "$base.jpg"
printf '%%s\t%%s\n' %[2]q %[1]q
`, info["title"], info["id"], infoPath)
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return script
}

func TestDownloadVideoNamesNewFilesAsEpisodesAndWritesEpisodeNFO(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DownloadDir = t.TempDir()
	cfg.YtDlp.Path = fakeDownloadYtDlp(t, map[string]any{
		"id":          "IuhG712NOak",
		"title":       "Chess Tournament - SNL",
		"description": "A student's parents interrupt a chess tournament.",
		"uploader":    "Saturday Night Live",
		"upload_date": "20261011",
		"timestamp":   snlUpload.Unix(),
		"duration":    269,
	})
	channelDir := filepath.Join(cfg.DownloadDir, "Saturday_Night_Live")
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Earlier downloads with the old naming, which Jellyfin has already indexed.
	legacy := []string{"2026-10-11 CopTalkShowSNL-RbXnGCrYNKs.mp4", "2026-10-11 CopTalkShowSNL-RbXnGCrYNKs.info.nfo", "2026-10-11 CopTalkShowSNL-RbXnGCrYNKs.jpg"}
	writeFiles(t, channelDir, legacy...)

	result, err := NewDownloader(cfg).DownloadVideo("https://www.youtube.com/watch?v=IuhG712NOak", "IuhG712NOak", "Saturday Night Live", "720", "mp4", false, false)
	if err != nil || !result.Downloaded {
		t.Fatalf("DownloadVideo() = %+v, %v", result, err)
	}

	stem := "S2026E1011044228 ChessTournamentSNL-IuhG712NOak"
	want := append([]string{}, legacy...)
	want = append(want, stem+".info.json", stem+".jpg", stem+".mp4", stem+".nfo")
	sort.Strings(want)
	if got := dirNames(t, channelDir); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("files = %q\nwant %q", got, want)
	}

	nfo, err := os.ReadFile(filepath.Join(channelDir, stem+".nfo"))
	if err != nil {
		t.Fatalf("read nfo: %v", err)
	}
	for _, check := range []string{
		"<episodedetails>",
		"<title>Chess Tournament - SNL</title>",
		"<showtitle>Saturday Night Live</showtitle>",
		"<season>2026</season>",
		"<episode>1011044228</episode>",
		"<aired>2026-10-11</aired>",
		`<uniqueid type="youtube" default="true">IuhG712NOak</uniqueid>`,
		"<thumb>" + stem + ".jpg</thumb>",
		"</episodedetails>",
	} {
		if !strings.Contains(string(nfo), check) {
			t.Errorf("NFO is missing %q:\n%s", check, nfo)
		}
	}
	if strings.Contains(string(nfo), "<movie>") {
		t.Error("NFO still uses the movie root element")
	}
}

func TestGenerateNFOFileIsNamedAfterTheVideoNotTheInfoJSON(t *testing.T) {
	// Regression: the NFO used to be named after the first file matching the ID, which
	// sorts as the .info.json, producing "<name>.info.nfo" that Jellyfin never reads.
	dir := t.TempDir()
	writeFiles(t, dir, "2026-10-11 ChessTournamentSNL-IuhG712NOak.info.json", "2026-10-11 ChessTournamentSNL-IuhG712NOak.jpg", "2026-10-11 ChessTournamentSNL-IuhG712NOak.mp4")

	err := NewDownloader(DefaultConfig()).generateNFOFile(dir, &VideoMetadata{ID: "IuhG712NOak", Title: "Chess Tournament - SNL", Uploader: "Saturday Night Live", UploadDate: "2026-10-11"})
	if err != nil {
		t.Fatalf("generateNFOFile() error = %v", err)
	}

	nfo, err := os.ReadFile(filepath.Join(dir, "2026-10-11 ChessTournamentSNL-IuhG712NOak.nfo"))
	if err != nil {
		t.Fatalf("want <video name>.nfo: %v (files: %q)", err, dirNames(t, dir))
	}
	if strings.Contains(string(nfo), "<season>") {
		t.Error("a legacy-named video must not get season/episode numbers that don't match its filename")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-11 ChessTournamentSNL-IuhG712NOak.info.nfo")); err == nil {
		t.Error("an .info.nfo file was written")
	}
}

func TestBuildFormatStringPrefersH264AndAACForMP4(t *testing.T) {
	d := NewDownloader(DefaultConfig())

	if got, want := d.buildFormatString("720", "mp4"), "bestvideo[height<=720][vcodec^=avc1]+bestaudio[acodec^=mp4a]/bestvideo[height<=720][vcodec^=avc1]+bestaudio/bestvideo[height<=720]+bestaudio/best"; got != want {
		t.Errorf("mp4 720 = %q\nwant %q", got, want)
	}
	if got, want := d.buildFormatString("best", "MP4"), "bestvideo[vcodec^=avc1]+bestaudio[acodec^=mp4a]/bestvideo[vcodec^=avc1]+bestaudio/bestvideo+bestaudio/best"; got != want {
		t.Errorf("mp4 best = %q\nwant %q", got, want)
	}
	// The config default format is mp4, so an unset format prefers H.264 too.
	if got := d.buildFormatString("1080", ""); !strings.HasPrefix(got, "bestvideo[height<=1080][vcodec^=avc1]") {
		t.Errorf("default format = %q, want the H.264 preference", got)
	}
	for _, format := range []string{"webm", "mkv"} {
		if got := d.buildFormatString("720", format); got != "bestvideo[height<=720]+bestaudio/best" {
			t.Errorf("%s = %q, want the unrestricted selector", format, got)
		}
	}
}

func TestPruningHandlesLegacyAndEpisodeNamedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	channelDir := filepath.Join(tmpDir, "Saturday_Night_Live")
	if err := os.MkdirAll(channelDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	legacyExpired := []string{"2026-09-01 OldSketchSNL-legacyOld01.mp4", "2026-09-01 OldSketchSNL-legacyOld01.info.json", "2026-09-01 OldSketchSNL-legacyOld01.info.nfo", "2026-09-01 OldSketchSNL-legacyOld01.jpg"}
	episodeExpired := []string{"S2026E0902120000 NewSketchSNL-episodeOld1.mp4", "S2026E0902120000 NewSketchSNL-episodeOld1.info.json", "S2026E0902120000 NewSketchSNL-episodeOld1.nfo", "S2026E0902120000 NewSketchSNL-episodeOld1.jpg"}
	episodeRecent := []string{"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4", "S2026E1011044228 ChessTournamentSNL-IuhG712NOak.nfo"}
	writeFiles(t, channelDir, append(append(append([]string{}, legacyExpired...), episodeExpired...), episodeRecent...)...)

	now := time.Now()
	if err := store.AddChannel(storage.Channel{
		ID:   "UCsnl",
		Name: "Saturday Night Live",
		DownloadedVideos: []storage.DownloadedVideo{
			{ID: "legacyOld01", Title: "Old Sketch", DownloadDate: now.AddDate(0, 0, -40)},
			{ID: "episodeOld1", Title: "New Sketch", DownloadDate: now.AddDate(0, 0, -40)},
			{ID: "IuhG712NOak", Title: "Chess Tournament", DownloadDate: now.AddDate(0, 0, -1)},
		},
	}); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}

	if err := NewDownloader(cfg).CleanOldVideosForChannel("Saturday Night Live", "UCsnl", 30, time.Time{}, store); err != nil {
		t.Fatalf("CleanOldVideosForChannel() error = %v", err)
	}

	if got := dirNames(t, channelDir); strings.Join(got, "|") != strings.Join(episodeRecent, "|") {
		t.Fatalf("files after pruning = %q, want only %q", got, episodeRecent)
	}
	ch, _ := store.GetChannel("UCsnl")
	if len(ch.DownloadedVideos) != 1 || ch.DownloadedVideos[0].ID != "IuhG712NOak" {
		t.Fatalf("downloaded videos after pruning = %+v", ch.DownloadedVideos)
	}
}

func TestStartupNoPruneMoveKeepsEpisodeNames(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DownloadDir = tmpDir
	cfg.RetentionDays = 30
	store, err := storage.NewStorage(filepath.Join(tmpDir, "data.db"))
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	channel := storage.Channel{
		ID:               "UCkeep",
		Name:             "Keep Channel é",
		RetentionDays:    30,
		DownloadedVideos: []storage.DownloadedVideo{{ID: "IuhG712NOak", Title: "Keep", DisablePruning: true}},
	}
	if err := store.AddChannel(channel); err != nil {
		t.Fatalf("AddChannel() error = %v", err)
	}
	legacyDir := filepath.Join(tmpDir, legacySanitizeFilename(channel.Name))
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	names := []string{"S2026E1011044228 ChessTournamentSNL-IuhG712NOak.mp4", "S2026E1011044228 ChessTournamentSNL-IuhG712NOak.nfo"}
	writeFiles(t, legacyDir, names...)

	runStartupChannelPruneScanAt(time.Now(), cfg, store)

	if got := dirNames(t, filepath.Join(tmpDir, sanitizeFilename(channel.Name))); strings.Join(got, "|") != strings.Join(names, "|") {
		t.Fatalf("moved files = %q, want the same names %q", got, names)
	}
}
