package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "no special characters", input: "normal_filename", expected: "normal_filename"},
		{name: "with forward slash", input: "path/to/file", expected: "path_to_file"},
		{name: "with backslash", input: "path\\to\\file", expected: "path_to_file"},
		{name: "with colon", input: "C:\\Users\\test", expected: "C_Users_test"},
		{name: "with multiple invalid chars", input: "video:title?with*special<chars>", expected: "video_title_with_special_chars"},
		{name: "with quotes and pipes", input: "test\"file\"|name", expected: "test_file_name"},
		{name: "empty string", input: "", expected: "unnamed"},
		{name: "only spaces", input: "   ", expected: "unnamed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSplitPortableFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantStem string
		wantExt  string
	}{
		{name: "simple ext", input: "video.mp4", wantStem: "video", wantExt: ".mp4"},
		{name: "info json ext", input: "video.info.json", wantStem: "video", wantExt: ".info.json"},
		{name: "no ext", input: "video", wantStem: "video", wantExt: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStem, gotExt := splitPortableFilename(tt.input)
			if gotStem != tt.wantStem || gotExt != tt.wantExt {
				t.Fatalf("splitPortableFilename(%q) = (%q, %q), want (%q, %q)", tt.input, gotStem, gotExt, tt.wantStem, tt.wantExt)
			}
		})
	}
}

func TestNormalizePortableFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "video filename", input: "2026-04-27 My Vidéo! #1-abc123.mp4", expected: "2026-04-27_My_Vid_o_1-abc123.mp4"},
		{name: "info json filename", input: "2026-04-27 My Vidéo! #1-abc123.info.json", expected: "2026-04-27_My_Vid_o_1-abc123.info.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizePortableFilename(tt.input)
			if got != tt.expected {
				t.Fatalf("normalizePortableFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestNormalizeDownloadedFilenames(t *testing.T) {
	tmpDir := t.TempDir()

	config := DefaultConfig()
	config.DownloadDir = tmpDir
	downloader := NewDownloader(config)

	videoID := "Abc-123_Xy"
	mediaOld := "2026-04-27 My Vidéo! #1-" + videoID + ".mp4"
	infoOld := "2026-04-27 My Vidéo! #1-" + videoID + ".info.json"
	unrelated := "leave-this-file-alone-otherid.mp4"

	for _, name := range []string{mediaOld, infoOld, unrelated} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}

	if err := downloader.normalizeDownloadedFilenames(tmpDir, videoID); err != nil {
		t.Fatalf("normalizeDownloadedFilenames() error = %v", err)
	}

	mediaNew := "2026-04-27_My_Vid_o_1-" + videoID + ".mp4"
	infoNew := "2026-04-27_My_Vid_o_1-" + videoID + ".info.json"

	if _, err := os.Stat(filepath.Join(tmpDir, mediaOld)); !os.IsNotExist(err) {
		t.Fatalf("expected old media filename to be renamed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, infoOld)); !os.IsNotExist(err) {
		t.Fatalf("expected old info filename to be renamed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, mediaNew)); err != nil {
		t.Fatalf("expected normalized media filename, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, infoNew)); err != nil {
		t.Fatalf("expected normalized info filename, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, unrelated)); err != nil {
		t.Fatalf("expected unrelated file to remain unchanged, stat err = %v", err)
	}
}
