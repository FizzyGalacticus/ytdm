package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSponsorBlockMarkSetters(t *testing.T) {
	store, err := NewStorage(filepath.Join(t.TempDir(), "sb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.AddChannel(Channel{ID: "UCsb", URL: "https://www.youtube.com/channel/UCsb", Name: "SB"}); err != nil {
		t.Fatal(err)
	}
	if ch, _ := store.GetChannel("UCsb"); ch.SponsorBlockMark {
		t.Fatal("new channel should default to sponsorblock_mark off")
	}
	if err := store.SetChannelSponsorBlockMark("UCsb", true); err != nil {
		t.Fatal(err)
	}
	if ch, _ := store.GetChannel("UCsb"); !ch.SponsorBlockMark {
		t.Fatal("SetChannelSponsorBlockMark(true) not stored")
	}
	// Other channel settings updates leave it alone.
	if err := store.UpdateChannel("UCsb", 4, false, time.Time{}, "", "", false, false); err != nil {
		t.Fatal(err)
	}
	if ch, _ := store.GetChannel("UCsb"); !ch.SponsorBlockMark || ch.RetentionDays != 4 {
		t.Fatalf("UpdateChannel changed sponsorblock_mark: %+v", ch)
	}

	if err := store.AddVideo(Video{ID: "vidsb000001", URL: "https://www.youtube.com/watch?v=vidsb000001", Title: "SB"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVideoSponsorBlockMark("vidsb000001", true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateVideo("vidsb000001", 2, false, "", "", true); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.GetVideo("vidsb000001"); !v.SponsorBlockMark || v.RetentionDays != 2 {
		t.Fatalf("video = %+v", v)
	}
	if err := store.SetVideoSponsorBlockMark("vidsb000001", false); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.GetVideo("vidsb000001"); v.SponsorBlockMark {
		t.Fatal("SetVideoSponsorBlockMark(false) not stored")
	}
}

func TestPing(t *testing.T) {
	store, err := NewStorage(filepath.Join(t.TempDir(), "ping.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() = %v", err)
	}
	store.Close()
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("Ping() on a closed database = nil, want an error")
	}
}
