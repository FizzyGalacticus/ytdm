package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaServerPath(t *testing.T) {
	dl := t.TempDir()
	file := filepath.Join(dl, "Some_Channel", "S2026E1120181033 Title-abcdefghijk.jpg")
	cases := []struct{ mediaDir, want string }{
		{"/youtube", "/youtube/Some_Channel/S2026E1120181033 Title-abcdefghijk.jpg"},
		{"/youtube/", "/youtube/Some_Channel/S2026E1120181033 Title-abcdefghijk.jpg"},
		{`D:\Media\YouTube`, `D:\Media\YouTube\Some_Channel\S2026E1120181033 Title-abcdefghijk.jpg`},
		{"D:/Media/YouTube", "D:/Media/YouTube/Some_Channel/S2026E1120181033 Title-abcdefghijk.jpg"},
		{"", file},
		{"  ", file},
	}
	for _, c := range cases {
		if got := mediaServerPath(dl, c.mediaDir, file); got != c.want {
			t.Errorf("mediaServerPath(%q) = %q, want %q", c.mediaDir, got, c.want)
		}
	}
	// Files outside the download directory keep ytdm's own absolute path.
	outside := filepath.Join(t.TempDir(), "x.jpg")
	if got := mediaServerPath(dl, "/youtube", outside); got != outside {
		t.Errorf("outside file = %q", got)
	}
}

const repairNFOTemplate = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<episodedetails>
  <title>Tom &amp; Jerry</title>
%s  <thumb aspect="poster">https://i.ytimg.com/vi/abcdefghijk/maxresdefault.jpg</thumb>
  <actor>
    <name>Chan</name>
    <thumb>https://yt3.ggpht.com/icon.jpg</thumb>
  </actor>
</episodedetails>
`

func TestRepairNFOThumbPaths(t *testing.T) {
	dl := t.TempDir()
	dir := filepath.Join(dl, "Chan")
	os.MkdirAll(dir, 0755)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	nfoWith := func(thumb string) string {
		if thumb == "" {
			return strings.Replace(repairNFOTemplate, "%s", "", 1)
		}
		return strings.Replace(repairNFOTemplate, "%s", "  <thumb>"+thumb+"</thumb>\n", 1)
	}

	// Bare filename, as older ytdm wrote it.
	write("A & B-aaaaaaaaaaa.jpg", "img")
	write("A & B-aaaaaaaaaaa.nfo", nfoWith("A &amp; B-aaaaaaaaaaa.jpg"))
	// A path from another mount whose image has a different stem (legacy naming).
	write("2026-01-01 Old-bbbbbbbbbbb.jpg", "img")
	write("2026-01-01 Old-bbbbbbbbbbb.info.nfo", nfoWith("/app/downloads/Chan/2026-01-01 Old-bbbbbbbbbbb.jpg"))
	// Image gone: the thumb is dropped.
	write("Gone-ccccccccccc.nfo", nfoWith("Gone-ccccccccccc.jpg"))
	// No local thumb line at all: left alone.
	write("NoThumb-ddddddddddd.nfo", nfoWith(""))
	// Not an episode NFO: left alone.
	write("tvshow.nfo", "<tvshow>\n  <thumb>poster.jpg</thumb>\n</tvshow>\n")

	cfg := DefaultConfig()
	cfg.DownloadDir = dl
	cfg.MediaServerDownloadDir = "/youtube"
	if n := RepairNFOThumbPaths(cfg); n != 3 {
		t.Fatalf("repaired %d files, want 3", n)
	}

	if got := read("A & B-aaaaaaaaaaa.nfo"); !strings.Contains(got, "  <thumb>/youtube/Chan/A &amp; B-aaaaaaaaaaa.jpg</thumb>\n") {
		t.Fatalf("bare filename not fixed:\n%s", got)
	}
	if got := read("2026-01-01 Old-bbbbbbbbbbb.info.nfo"); !strings.Contains(got, "  <thumb>/youtube/Chan/2026-01-01 Old-bbbbbbbbbbb.jpg</thumb>\n") {
		t.Fatalf("other-mount path not fixed:\n%s", got)
	}
	gone := read("Gone-ccccccccccc.nfo")
	if gone != nfoWith("") {
		t.Fatalf("missing image's thumb not dropped:\n%s", gone)
	}
	if got := read("NoThumb-ddddddddddd.nfo"); got != nfoWith("") {
		t.Fatalf("NFO without a local thumb changed:\n%s", got)
	}
	if got := read("tvshow.nfo"); !strings.Contains(got, "<thumb>poster.jpg</thumb>") {
		t.Fatalf("non-episode NFO changed:\n%s", got)
	}
	// The poster URL and channel icon are untouched.
	for _, name := range []string{"A & B-aaaaaaaaaaa.nfo", "Gone-ccccccccccc.nfo"} {
		got := read(name)
		if !strings.Contains(got, `<thumb aspect="poster">https://i.ytimg.com/`) || !strings.Contains(got, "    <thumb>https://yt3.ggpht.com/icon.jpg</thumb>") {
			t.Fatalf("%s lost its remote thumbs:\n%s", name, got)
		}
	}

	// Running again changes nothing; changing the setting rewrites the paths.
	if n := RepairNFOThumbPaths(cfg); n != 0 {
		t.Fatalf("second pass repaired %d files", n)
	}
	cfg.MediaServerDownloadDir = "/media/yt"
	if n := RepairNFOThumbPaths(cfg); n != 2 {
		t.Fatalf("after changing the path repaired %d files, want 2", n)
	}
	if got := read("A & B-aaaaaaaaaaa.nfo"); !strings.Contains(got, "<thumb>/media/yt/Chan/A &amp; B-aaaaaaaaaaa.jpg</thumb>") {
		t.Fatalf("path change not applied:\n%s", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 7 {
		t.Fatalf("repair left stray files: %d entries", len(entries))
	}
}

func TestConfigAPIMediaServerDownloadDir(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(wd) })
	os.MkdirAll("data", 0755)

	cfg := newTestConfig(t)
	api := &APIServer{config: cfg, storage: newTestStorage(t)}
	put := func(body string) int {
		rec := httptest.NewRecorder()
		api.updateConfig(rec, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
		return rec.Code
	}
	if code := put(`{"media_server_download_dir": " /youtube "}`); code != 200 || cfg.MediaServerDownloadDir != "/youtube" {
		t.Fatalf("PUT = %d, dir %q", code, cfg.MediaServerDownloadDir)
	}
	if code := put(`{"media_server_download_dir": "relative/path"}`); code != 400 || cfg.MediaServerDownloadDir != "/youtube" {
		t.Fatalf("relative PUT = %d, dir %q", code, cfg.MediaServerDownloadDir)
	}
	if code := put(`{"media_server_download_dir": "D:\\Media\\YouTube"}`); code != 200 {
		t.Fatalf("Windows path PUT = %d", code)
	}
	if code := put(`{"media_server_download_dir": ""}`); code != 200 || cfg.MediaServerDownloadDir != "" {
		t.Fatalf("clearing PUT = %d, dir %q", code, cfg.MediaServerDownloadDir)
	}
}
