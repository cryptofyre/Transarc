package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/live"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/probe"
	"github.com/cryptofyre/transarc/internal/state"
)

func TestProcessReportsToHub(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	path := filepath.Join(dir, "Show.mkv")
	var buf bytes.Buffer
	hub := live.New(32)
	r := &Runner{
		Cfg:       config.Config{TempDir: dir, Rules: config.Rules{SampleSeconds: -1}},
		Log:       logx.New(&buf),
		Store:     store,
		Sessions:  &playThenStop{stopAfter: 1},
		PlayEvery: 5 * time.Millisecond,
		Live:      hub,
	}
	kind, _ := r.process(context.Background(), "/dev/dri/renderD128", candidate{
		path:  path,
		size:  100,
		media: probe.Media{Video: &probe.Video{Codec: "hevc", Width: 1920, Height: 1080}},
	}, Options{})
	if kind != "failed" {
		t.Fatalf("kind %q\n%s", kind, buf.String())
	}
	var got []string
	for _, e := range hub.Recent() {
		got = append(got, e.Type)
	}
	want := []string{live.JobStart, live.JobPhase, live.JobFinish}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("events %v", got)
	}
	es := hub.Recent()
	if start := es[0].Data.(live.Job); start.Device != "D128" || start.Codec != "hevc" || start.Path != path {
		t.Fatalf("start %+v", start)
	}
	if ph := es[1].Data.(live.PhaseChange); ph.Phase != live.PhaseEncode {
		t.Fatalf("phase %+v", ph)
	}
	end := es[2].Data.(live.JobEnd)
	if end.Outcome != "failed" || end.Reason == "" || end.SourceBytes != 100 {
		t.Fatalf("end %+v", end)
	}
	if len(hub.Jobs()) != 0 {
		t.Fatal("job left running")
	}
}

// sockDB is a state_db path for Serve's control socket. Windows cannot always
// unlink a unix socket file, so its folder is removed best effort.
func sockDB(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "transarc-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "transarc.db")
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestServeAPIStopsWithOpenStream(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "transarc.db")
	store, err := state.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	addr := freeAddr(t)
	r := &Runner{
		Cfg: config.Config{
			Schedule: "0 0 1 1 *",
			Timezone: "UTC",
			StateDB:  sockDB(t),
			TempDir:  dir,
			Rules:    config.Rules{MinSavingsPercent: 10, SampleSeconds: -1},
			API:      config.API{Listen: addr, Token: "t0k"},
		},
		Log:     logx.New(io.Discard),
		Store:   store,
		Backend: "svt",
		GPUs:    []string{"cpu", "cpu"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- r.Serve(ctx) }()

	base := "http://" + addr
	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/status", nil)
		req.Header.Set("Authorization", "Bearer t0k")
		resp, err = http.DefaultClient.Do(req)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Backend string `json:"backend"`
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
		Pass struct {
			Next *time.Time `json:"next"`
		} `json:"pass"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.Backend != "svt" || len(st.Devices) != 1 || st.Devices[0].ID != "cpu" || st.Pass.Next == nil {
		t.Fatalf("status %+v", st)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer t0k")
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	line, err := bufio.NewReader(stream.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "event: status") {
		t.Fatalf("stream %q %v", line, err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return with an event stream open")
	}
}

func TestServeKeepsRunningWhenAPIPortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dir := t.TempDir()
	db := filepath.Join(dir, "transarc.db")
	store, err := state.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var buf syncBuffer
	r := &Runner{
		Cfg: config.Config{
			Schedule: "0 0 1 1 *", Timezone: "UTC", StateDB: sockDB(t), TempDir: dir,
			Rules: config.Rules{SampleSeconds: -1},
			API:   config.API{Listen: ln.Addr().String()},
		},
		Log: logx.New(&buf), Store: store, Backend: "svt", GPUs: []string{"cpu"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- r.Serve(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buf.String(), "api:") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), "api:") {
		t.Fatalf("no bind warning\n%s", buf.String())
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
