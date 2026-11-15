package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSkipRemembersEncodedAndRetriesKept(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mod := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := s.Put(Record{Path: "/a.mkv", Size: 10, ModUnix: mod.Unix(), Decision: "encoded", Reason: "savings 40%", SourceBytes: 10, OutputBytes: 6}); err != nil {
		t.Fatal(err)
	}
	skip, reason, err := s.ShouldSkip("/a.mkv", 10, mod, 24*time.Hour)
	if err != nil || !skip || reason == "" {
		t.Fatalf("encoded should skip: %v %q %v", skip, reason, err)
	}
	skip, _, err = s.ShouldSkip("/a.mkv", 11, mod, 24*time.Hour)
	if err != nil || skip {
		t.Fatal("changed size should not skip")
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := s.Put(Record{Path: "/b.mkv", Size: 10, ModUnix: mod.Unix(), Decision: "kept", Reason: "sample", CheckedUnix: old.Unix()}); err != nil {
		t.Fatal(err)
	}
	skip, _, err = s.ShouldSkip("/b.mkv", 10, mod, 24*time.Hour)
	if err != nil || skip {
		t.Fatal("expired keep should be retried")
	}
	if err := s.Put(Record{Path: "/b.mkv", Size: 10, ModUnix: mod.Unix(), Decision: "kept", Reason: "sample", CheckedUnix: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	skip, _, err = s.ShouldSkip("/b.mkv", 10, mod, 24*time.Hour)
	if err != nil || !skip {
		t.Fatal("fresh keep should skip")
	}
	if err := s.Put(Record{Path: "/c.mkv", Size: 10, ModUnix: mod.Unix(), Decision: "queued", Reason: "candidate"}); err != nil {
		t.Fatal(err)
	}
	skip, _, err = s.ShouldSkip("/c.mkv", 10, mod, 24*time.Hour)
	if err != nil || skip {
		t.Fatal("queued file stays in the encode pass")
	}
}

func TestPrescanRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, ok, err := s.LoadPrescan()
	if err != nil || ok {
		t.Fatalf("empty prescan ok=%v err=%v", ok, err)
	}
	in := Prescan{
		Finished:   time.Unix(1_700_000_000, 0),
		Files:      10,
		Candidates: 2,
		Skipped:    8,
		Reasons:    []ReasonCount{{Reason: "already av1", N: 5}, {Reason: "below minimum size", N: 3}},
	}
	if err := s.SavePrescan(in); err != nil {
		t.Fatal(err)
	}
	out, ok, err := s.LoadPrescan()
	if err != nil || !ok || out.Candidates != 2 || out.Skipped != 8 || len(out.Reasons) != 2 || out.Reasons[0].Reason != "already av1" {
		t.Fatalf("%+v ok=%v err=%v", out, ok, err)
	}
}
