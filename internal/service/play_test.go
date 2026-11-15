package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/probe"
	"github.com/cryptofyre/transarc/internal/state"
)

func TestProcessWaitsWhilePlaying(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	path := filepath.Join(dir, "Show.mkv")
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		Log:       logx.New(&buf),
		Store:     store,
		Sessions:  playAlways{},
		PlayEvery: 5 * time.Millisecond,
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	kind, saved := r.process(ctx, "/dev/dri/renderD128", candidate{path: path, size: 100}, Options{})
	if kind != "" || saved != 0 {
		t.Fatalf("kind %q saved %d", kind, saved)
	}
	if strings.Contains(buf.String(), "FILE") {
		t.Fatalf("encode started\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "pausing until playback stops") {
		t.Fatalf("log\n%s", buf.String())
	}
	if _, ok, err := store.Get(path); err != nil || ok {
		t.Fatalf("stored a transient wait ok=%v err=%v", ok, err)
	}
}

func TestProcessContinuesAfterPlaybackStops(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	path := filepath.Join(dir, "Show.mkv")
	var buf bytes.Buffer
	r := &Runner{
		Cfg:       config.Config{Rules: config.Rules{SampleSeconds: -1}},
		Log:       logx.New(&buf),
		Store:     store,
		Sessions:  &playThenStop{stopAfter: 1},
		PlayEvery: 5 * time.Millisecond,
	}
	r.Cfg.TempDir = dir
	kind, saved := r.process(context.Background(), "/dev/dri/renderD128", candidate{
		path:  path,
		size:  100,
		media: probe.Media{Video: &probe.Video{Codec: "hevc", Width: 1920, Height: 1080}},
	}, Options{})
	if kind != "failed" || saved != 0 {
		t.Fatalf("kind %q saved %d\n%s", kind, saved, buf.String())
	}
	log := buf.String()
	if !strings.Contains(log, "pausing until playback stops") || !strings.Contains(log, "playback stopped, continuing") || !strings.Contains(log, "FILE") {
		t.Fatalf("log\n%s", log)
	}
	rec, ok, err := store.Get(path)
	if err != nil || !ok || rec.Decision != "failed" {
		t.Fatalf("decision %+v ok=%v err=%v", rec, ok, err)
	}
}

func TestPlaybackErrorDoesNotSkip(t *testing.T) {
	var buf bytes.Buffer
	r := &Runner{Log: logx.New(&buf), Sessions: playFail{}}
	if r.busy(context.Background(), "a.mkv") || r.busy(context.Background(), "b.mkv") {
		t.Fatal("a down server skipped the file")
	}
	if strings.Count(buf.String(), "playback:") != 1 {
		t.Fatalf("warnings\n%s", buf.String())
	}
}

func TestRunWatchedStopsEncode(t *testing.T) {
	r := &Runner{
		Log:       logx.New(io.Discard),
		Sessions:  &playAfter{after: 1},
		PlayEvery: 10 * time.Millisecond,
	}
	start := time.Now()
	err := r.runWatched(context.Background(), "movie.mkv", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, errWatching) {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("encode was not stopped")
	}
}

func TestRunWatchedShutdownIsNotPlayback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		Log:       logx.New(io.Discard),
		Sessions:  playNever{},
		PlayEvery: time.Hour,
	}
	done := make(chan error, 1)
	go func() {
		done <- r.runWatched(ctx, "movie.mkv", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if errors.Is(err, errWatching) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not return")
	}
}

type playAlways struct{}

func (playAlways) Playing(context.Context, string) (bool, error) { return true, nil }

type playThenStop struct {
	mu        sync.Mutex
	n         int
	stopAfter int
}

func (p *playThenStop) Playing(context.Context, string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	return p.n <= p.stopAfter, nil
}

func (p *playThenStop) Refresh(ctx context.Context, path string) (bool, error) {
	return p.Playing(ctx, path)
}

type playNever struct{}

func (playNever) Playing(context.Context, string) (bool, error) { return false, nil }
func (playNever) Refresh(context.Context, string) (bool, error) { return false, nil }

type playFail struct{}

func (playFail) Playing(context.Context, string) (bool, error) {
	return false, errors.New("plex /status/sessions: HTTP 500")
}

type playAfter struct {
	mu    sync.Mutex
	n     int
	after int
}

func (p *playAfter) Playing(context.Context, string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	return p.n >= p.after, nil
}

func (p *playAfter) Refresh(ctx context.Context, path string) (bool, error) {
	return p.Playing(ctx, path)
}
