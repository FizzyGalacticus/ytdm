package main

import (
	"testing"
)

func TestLogBufferParsesScopedEntries(t *testing.T) {
	lb := NewLogBuffer(10)

	_, _ = lb.Write([]byte("2026/05/31 12:00:00 scheduler.go:1: [scope:channel:UCabc:My Channel] Finished channel\n"))
	_, _ = lb.Write([]byte("2026/05/31 12:00:01 scheduler.go:2: plain message\n"))

	entries := lb.GetStructuredEntries("", "")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].ScopeType != "channel" || entries[0].ScopeID != "UCabc" || entries[0].ScopeName != "My Channel" {
		t.Fatalf("unexpected scope metadata: %+v", entries[0])
	}
	if entries[0].Line == "" || entries[0].Line == "[scope:channel:UCabc:My Channel]" {
		t.Fatalf("expected stripped line content, got %q", entries[0].Line)
	}

	if entries[1].ScopeType != "" || entries[1].ScopeID != "" {
		t.Fatalf("expected unscoped entry, got %+v", entries[1])
	}
}

func TestLogBufferScopeFilteringAndScopes(t *testing.T) {
	lb := NewLogBuffer(10)

	_, _ = lb.Write([]byte("x [scope:channel:UC1:Channel One] msg\n"))
	_, _ = lb.Write([]byte("x [scope:video:vid1:Video One] msg\n"))
	_, _ = lb.Write([]byte("x [scope:channel:UC1:Channel One] msg2\n"))

	channelEntries := lb.GetStructuredEntries("channel", "UC1")
	if len(channelEntries) != 2 {
		t.Fatalf("expected 2 channel entries, got %d", len(channelEntries))
	}

	videoEntries := lb.GetStructuredEntries("video", "vid1")
	if len(videoEntries) != 1 {
		t.Fatalf("expected 1 video entry, got %d", len(videoEntries))
	}

	scopes := lb.GetScopes()
	if len(scopes) != 2 {
		t.Fatalf("expected 2 unique scopes, got %d", len(scopes))
	}
}
