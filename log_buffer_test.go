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

	var scoped, unscoped *LogEntry
	for i := range entries {
		if entries[i].ScopeType != "" {
			scoped = &entries[i]
		} else {
			unscoped = &entries[i]
		}
	}

	if scoped == nil {
		t.Fatal("expected a scoped entry, found none")
	}
	if scoped.ScopeType != "channel" || scoped.ScopeID != "UCabc" || scoped.ScopeName != "My Channel" {
		t.Fatalf("unexpected scope metadata: %+v", *scoped)
	}
	if scoped.Line == "" || scoped.Line == "[scope:channel:UCabc:My Channel]" {
		t.Fatalf("expected stripped line content, got %q", scoped.Line)
	}

	if unscoped == nil {
		t.Fatal("expected an unscoped entry, found none")
	}
	if unscoped.ScopeType != "" || unscoped.ScopeID != "" {
		t.Fatalf("expected unscoped entry, got %+v", *unscoped)
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
