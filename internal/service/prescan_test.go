package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/probe"
	"github.com/cryptofyre/transarc/internal/state"
)

func TestPrescanUsesMarks(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	small := filepath.Join(dir, "small.mkv")
	if err := os.WriteFile(small, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(small, old, old); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.mkv")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1<<20 + 32); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chtimes(big, old, old); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(big)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(dir, "state", "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Put(state.Record{Path: big, Size: fi.Size(), ModUnix: fi.ModTime().Unix(), Decision: "queued", Reason: "candidate"}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{
		Cfg: config.Config{
			Libraries: []config.Library{{Name: "tv", Path: dir}},
			Rules: config.Rules{
				MinFileSizeMB:    1,
				Extensions:       []string{"mkv"},
				ProbeConcurrency: 2,
			},
		},
		Log:   logx.New(&nopWriter{}),
		Store: store,
		Probe: probe.Prober{Bin: filepath.Join(dir, "ffprobe-missing")},
	}
	p, err := r.prescan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Candidates != 1 || p.Skipped != 1 || p.Failed != 0 {
		t.Fatalf("candidates %d skipped %d failed %d reasons %+v", p.Candidates, p.Skipped, p.Failed, p.Reasons)
	}
	if len(p.Reasons) != 1 || p.Reasons[0].Reason != "below minimum size" || p.Reasons[0].N != 1 {
		t.Fatalf("reasons %+v", p.Reasons)
	}
	rec, ok, err := store.Get(small)
	if err != nil || !ok || rec.Decision != "skipped" {
		t.Fatalf("small mark %+v ok=%v err=%v", rec, ok, err)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
