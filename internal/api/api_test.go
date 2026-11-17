package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cryptofyre/transarc/internal/live"
	"github.com/cryptofyre/transarc/internal/state"
)

func testServer(t *testing.T, token string) (*Server, *state.Store) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	put := func(r state.Record) {
		if err := store.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	put(state.Record{Path: "/movies/A.mkv", Size: 4e9, Decision: "encoded", Reason: "ok", SavingsPct: 90, SourceBytes: 40e9, OutputBytes: 4e9, CheckedUnix: 300, GPU: "D128"})
	put(state.Record{Path: "/movies/B.mkv", Size: 10e9, Decision: "kept", Reason: "sample estimated 3%", SourceBytes: 10e9, CheckedUnix: 200, GPU: "D129"})
	put(state.Record{Path: "/movies/C.mkv", Size: 1e6, Decision: "skipped", Reason: "below minimum size", CheckedUnix: 250})
	put(state.Record{Path: "/movies/D.mkv", Size: 5e9, Decision: "failed", Reason: "ffmpeg", CheckedUnix: 100})
	put(state.Record{Path: "/movies/Q1.mkv", Size: 30e9, Decision: "queued", Reason: "candidate", CheckedUnix: 50})
	put(state.Record{Path: "/movies/Q2.mkv", Size: 50e9, Decision: "queued", Reason: "candidate", CheckedUnix: 60})
	s := &Server{
		Hub:     live.New(64),
		Store:   store,
		Token:   token,
		Version: "test",
		Backend: "qsv",
		Devices: []Device{{ID: "D128", Node: "renderD128", Name: "Intel Arc A750"}},
		Next:    func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) },
		Scan: func() (state.Prescan, bool) {
			return state.Prescan{Files: 900, Candidates: 40, Skipped: 860, Reasons: []state.ReasonCount{{Reason: "already AV1", N: 800}}}, true
		},
		Heartbeat: 50 * time.Millisecond,
	}
	return s, store
}

func get(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuth(t *testing.T) {
	s, _ := testServer(t, "secret")
	h := s.Handler()
	if rec := get(t, h, "/api/v1/health", ""); rec.Code != 200 {
		t.Fatalf("health %d", rec.Code)
	}
	for _, p := range []string{"/api/v1/status", "/api/v1/history", "/api/v1/queue", "/api/v1/events"} {
		if rec := get(t, h, p, ""); rec.Code != 401 {
			t.Fatalf("%s without token %d", p, rec.Code)
		}
		if rec := get(t, h, p, "wrong"); rec.Code != 401 {
			t.Fatalf("%s wrong token %d", p, rec.Code)
		}
	}
	if rec := get(t, h, "/api/v1/status", "secret"); rec.Code != 200 {
		t.Fatalf("status with token %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d", rec.Code)
	}
}

func TestStatus(t *testing.T) {
	s, _ := testServer(t, "")
	s.Hub.PassStarted(live.PassInfo{Candidates: 40, Devices: []string{"D128"}})
	s.Hub.JobStarted("/movies/Q2.mkv", live.Info{Device: "D128", SourceBytes: 50e9})
	s.Hub.Phase("/movies/Q2.mkv", live.PhaseEncode)
	s.Hub.Progress("/movies/Q2.mkv", 34.9, 75, 3.1, 2.2e9)
	rec := get(t, s.Handler(), "/api/v1/status", "")
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Pass.Running || st.Pass.Next == nil || st.Pass.Info.Candidates != 40 {
		t.Fatalf("pass %+v", st.Pass)
	}
	if len(st.Jobs) != 1 || st.Jobs[0].Percent != 34.9 || st.Jobs[0].Phase != "encode" || st.Jobs[0].Name != "Q2.mkv" {
		t.Fatalf("jobs %+v", st.Jobs)
	}
	if st.Totals == nil || st.Totals.Encoded != 1 || st.Totals.SavedBytes != 36e9 {
		t.Fatalf("totals %+v", st.Totals)
	}
	if st.Queue == nil || st.Queue.Candidates != 40 || len(st.Queue.Reasons) != 1 {
		t.Fatalf("queue %+v", st.Queue)
	}
	if st.Devices[0].Name != "Intel Arc A750" || st.Backend != "qsv" || st.Version != "test" {
		t.Fatalf("status %+v", st)
	}
}

type historyDoc struct {
	Items      []Item `json:"items"`
	NextBefore *int64 `json:"next_before"`
}

func TestHistory(t *testing.T) {
	s, _ := testServer(t, "")
	h := s.Handler()
	var doc historyDoc
	if err := json.Unmarshal(get(t, h, "/api/v1/history", "").Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, it := range doc.Items {
		names = append(names, it.Name)
	}
	if strings.Join(names, ",") != "A.mkv,B.mkv,D.mkv" || doc.NextBefore != nil {
		t.Fatalf("default history %v next %v", names, doc.NextBefore)
	}
	if doc.Items[0].SavedBytes != 36e9 || doc.Items[0].Device != "D128" || doc.Items[0].Checked.Unix() != 300 {
		t.Fatalf("item %+v", doc.Items[0])
	}

	doc = historyDoc{}
	_ = json.Unmarshal(get(t, h, "/api/v1/history?limit=1", "").Body.Bytes(), &doc)
	if len(doc.Items) != 1 || doc.NextBefore == nil || *doc.NextBefore != 300 {
		t.Fatalf("page 1 %+v", doc)
	}
	doc = historyDoc{}
	_ = json.Unmarshal(get(t, h, "/api/v1/history?limit=1&before=300", "").Body.Bytes(), &doc)
	if len(doc.Items) != 1 || doc.Items[0].Name != "B.mkv" {
		t.Fatalf("page 2 %+v", doc)
	}

	doc = historyDoc{}
	_ = json.Unmarshal(get(t, h, "/api/v1/history?decision=skipped", "").Body.Bytes(), &doc)
	if len(doc.Items) != 1 || doc.Items[0].Name != "C.mkv" {
		t.Fatalf("skipped %+v", doc)
	}
	doc = historyDoc{}
	_ = json.Unmarshal(get(t, h, "/api/v1/history?decision=all", "").Body.Bytes(), &doc)
	if len(doc.Items) != 6 {
		t.Fatalf("all %d", len(doc.Items))
	}
	if rec := get(t, h, "/api/v1/history?decision=bogus", ""); rec.Code != 400 {
		t.Fatalf("bad decision %d", rec.Code)
	}
	if rec := get(t, h, "/api/v1/history?limit=0", ""); rec.Code != 400 {
		t.Fatalf("bad limit %d", rec.Code)
	}
}

func TestQueue(t *testing.T) {
	s, _ := testServer(t, "")
	var doc struct {
		Items []Item `json:"items"`
	}
	_ = json.Unmarshal(get(t, s.Handler(), "/api/v1/queue", "").Body.Bytes(), &doc)
	if len(doc.Items) != 2 || doc.Items[0].Name != "Q2.mkv" {
		t.Fatalf("queue %+v", doc.Items)
	}
}

type sseEvent struct {
	id, typ, data string
}

func readEvent(t *testing.T, br *bufio.Reader) sseEvent {
	t.Helper()
	var e sseEvent
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (partial %+v)", err, e)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if e.typ != "" {
				return e
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			e.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			e.typ = line[7:]
		case strings.HasPrefix(line, "data: "):
			e.data = line[6:]
		}
	}
}

func openStream(t *testing.T, url, token, lastID string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url+"/api/v1/events", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return resp, bufio.NewReader(resp.Body)
}

func TestEventsStream(t *testing.T) {
	s, _ := testServer(t, "secret")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	s.Hub.PrescanDone(live.PrescanInfo{Files: 1})
	resp, br := openStream(t, ts.URL, "secret", "")
	defer resp.Body.Close()
	first := readEvent(t, br)
	if first.typ != "status" || first.id != "" || !strings.Contains(first.data, `"backend":"qsv"`) {
		t.Fatalf("first %+v", first)
	}
	s.Hub.JobStarted("/movies/Q2.mkv", live.Info{Device: "D128"})
	got := readEvent(t, br)
	if got.typ != live.JobStart || got.id != "2" {
		t.Fatalf("event %+v", got)
	}
	var e live.Event
	if err := json.Unmarshal([]byte(got.data), &e); err != nil || e.Name != "Q2.mkv" || e.Path != "/movies/Q2.mkv" {
		t.Fatalf("data %s %v", got.data, err)
	}

	resp2, br2 := openStream(t, ts.URL, "secret", "1")
	defer resp2.Body.Close()
	if ev := readEvent(t, br2); ev.typ != "status" {
		t.Fatalf("replay first %+v", ev)
	}
	if ev := readEvent(t, br2); ev.typ != live.JobStart || ev.id != "2" {
		t.Fatalf("replay %+v", ev)
	}
}

func TestListenShutdownEndsOpenStreams(t *testing.T) {
	s, _ := testServer(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	addr, done, err := s.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Listen(ctx, addr.String()); err == nil {
		t.Fatal("second bind on the same port succeeded")
	}
	resp, br := openStream(t, "http://"+addr.String(), "", "")
	defer resp.Body.Close()
	readEvent(t, br)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop with a stream open")
	}
	if _, err := io.ReadAll(resp.Body); err != nil && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("stream end: %v", err)
	}
}
