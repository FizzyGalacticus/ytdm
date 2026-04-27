package main

import (
	"testing"
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
