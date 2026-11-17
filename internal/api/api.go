// Package api serves the running service's state as read-only JSON and a
// Server-Sent Events stream, for dashboards.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cryptofyre/transarc/internal/live"
	"github.com/cryptofyre/transarc/internal/state"
)

// Device is one encoder slot.
type Device struct {
	ID   string `json:"id"`
	Node string `json:"node"`
	Name string `json:"name,omitempty"`
}

// Server answers the API. Next and Scan may be nil.
type Server struct {
	Hub     *live.Hub
	Store   *state.Store
	Token   string
	Version string
	Backend string
	Devices []Device
	Next    func() time.Time
	Scan    func() (state.Prescan, bool)
	// Heartbeat is the SSE keep-alive interval. Zero uses 15 seconds.
	Heartbeat time.Duration
}

// Listen binds addr now, so a taken port is reported at startup, then serves
// until ctx ends. Open event streams end with ctx.
func (s *Server) Listen(ctx context.Context, addr string) (net.Addr, <-chan error, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	go func() {
		<-ctx.Done()
		s.Hub.CloseAll()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			srv.Close()
		}
	}()
	return ln.Addr(), done, nil
}

// Handler routes the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.Handle("GET /api/v1/status", s.auth(s.status))
	mux.Handle("GET /api/v1/history", s.auth(s.history))
	mux.Handle("GET /api/v1/queue", s.auth(s.queue))
	mux.Handle("GET /api/v1/events", s.auth(s.events))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(s.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="transarc"`)
				writeError(w, http.StatusUnauthorized, "missing or wrong bearer token")
				return
			}
		}
		next(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version})
}

// Status is the whole picture at one moment.
type Status struct {
	Version string       `json:"version"`
	Backend string       `json:"backend"`
	Devices []Device     `json:"devices"`
	Pass    PassStatus   `json:"pass"`
	Jobs    []live.Job   `json:"jobs"`
	Totals  *Totals      `json:"totals"`
	Queue   *QueueStatus `json:"queue"`
	Time    time.Time    `json:"time"`
}

// PassStatus is the current or next pass.
type PassStatus struct {
	Running bool           `json:"running"`
	Started *time.Time     `json:"started,omitempty"`
	Info    *live.PassInfo `json:"info,omitempty"`
	Next    *time.Time     `json:"next,omitempty"`
}

// Totals are all-time counts from the state database.
type Totals struct {
	Encoded    int   `json:"encoded"`
	Kept       int   `json:"kept"`
	Skipped    int   `json:"skipped"`
	Failed     int   `json:"failed"`
	SavedBytes int64 `json:"saved_bytes"`
}

// QueueStatus is the last library count.
type QueueStatus struct {
	Finished   time.Time     `json:"finished"`
	Files      int           `json:"files"`
	Candidates int           `json:"candidates"`
	Skipped    int           `json:"skipped"`
	Settling   int           `json:"settling"`
	Failed     int           `json:"failed"`
	Reasons    []ReasonCount `json:"reasons"`
}

// ReasonCount is one skip bucket.
type ReasonCount struct {
	Reason string `json:"reason"`
	N      int    `json:"n"`
}

// Snapshot builds the status document.
func (s *Server) Snapshot() Status {
	st := Status{
		Version: s.Version,
		Backend: s.Backend,
		Devices: s.Devices,
		Jobs:    s.Hub.Jobs(),
		Time:    time.Now().UTC(),
	}
	if st.Devices == nil {
		st.Devices = []Device{}
	}
	if st.Jobs == nil {
		st.Jobs = []live.Job{}
	}
	p := s.Hub.Pass()
	st.Pass = PassStatus{Running: p.Running, Started: p.Started, Info: p.Info}
	if s.Next != nil {
		if n := s.Next(); !n.IsZero() {
			st.Pass.Next = &n
		}
	}
	if s.Store != nil {
		if sum, err := s.Store.Summary(); err == nil {
			st.Totals = &Totals{Encoded: sum.Encoded, Kept: sum.Kept, Skipped: sum.Skipped, Failed: sum.Failed, SavedBytes: sum.SavedBytes}
		}
	}
	if s.Scan != nil {
		if sc, ok := s.Scan(); ok {
			q := &QueueStatus{Finished: sc.Finished, Files: sc.Files, Candidates: sc.Candidates, Skipped: sc.Skipped, Settling: sc.Settling, Failed: sc.Failed, Reasons: []ReasonCount{}}
			for _, rc := range sc.Reasons {
				q.Reasons = append(q.Reasons, ReasonCount{Reason: rc.Reason, N: rc.N})
			}
			st.Queue = q
		}
	}
	return st
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Snapshot())
}

// Item is one file's latest decision.
type Item struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	Decision    string    `json:"decision"`
	Reason      string    `json:"reason"`
	Size        int64     `json:"size"`
	SourceBytes int64     `json:"source_bytes"`
	OutputBytes int64     `json:"output_bytes"`
	SavedBytes  int64     `json:"saved_bytes"`
	SavingsPct  float64   `json:"savings_pct"`
	Device      string    `json:"device"`
	Checked     time.Time `json:"checked"`
}

func toItem(r state.Record) Item {
	it := Item{
		Path: r.Path, Name: filepath.Base(r.Path), Decision: r.Decision, Reason: r.Reason,
		Size: r.Size, SourceBytes: r.SourceBytes, OutputBytes: r.OutputBytes,
		SavingsPct: r.SavingsPct, Device: r.GPU, Checked: time.Unix(r.CheckedUnix, 0).UTC(),
	}
	if r.Decision == "encoded" && r.SourceBytes > r.OutputBytes {
		it.SavedBytes = r.SourceBytes - r.OutputBytes
	}
	return it
}

var defaultDecisions = []string{"encoded", "kept", "failed"}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no state database")
		return
	}
	q := r.URL.Query()
	limit, err := intParam(q.Get("limit"), 50, 1, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit: "+err.Error())
		return
	}
	var before int64
	if v := q.Get("before"); v != "" {
		before, err = strconv.ParseInt(v, 10, 64)
		if err != nil || before < 0 {
			writeError(w, http.StatusBadRequest, "before must be a unix time in seconds")
			return
		}
	}
	decisions := defaultDecisions
	if v := strings.TrimSpace(q.Get("decision")); v != "" {
		decisions = nil
		if v != "all" {
			for _, d := range strings.Split(v, ",") {
				d = strings.TrimSpace(d)
				switch d {
				case "encoded", "kept", "failed", "skipped", "queued":
					decisions = append(decisions, d)
				case "":
				default:
					writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown decision %q", d))
					return
				}
			}
		}
	}
	recs, err := s.Store.Recent(limit, before, decisions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := struct {
		Items      []Item `json:"items"`
		NextBefore *int64 `json:"next_before"`
	}{Items: make([]Item, 0, len(recs))}
	for _, rec := range recs {
		out.Items = append(out.Items, toItem(rec))
	}
	if len(recs) == limit {
		nb := recs[len(recs)-1].CheckedUnix
		out.NextBefore = &nb
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no state database")
		return
	}
	limit, err := intParam(r.URL.Query().Get("limit"), 50, 1, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit: "+err.Error())
		return
	}
	recs, err := s.Store.Queued(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]Item, 0, len(recs))
	for _, rec := range recs {
		items = append(items, toItem(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	var since uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		since, _ = strconv.ParseUint(v, 10, 64)
	} else if v := r.URL.Query().Get("since"); v != "" {
		since, _ = strconv.ParseUint(v, 10, 64)
	}
	backlog, ch, cancel := s.Hub.Subscribe(since, 128)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := writeEvent(w, 0, "status", s.Snapshot()); err != nil {
		return
	}
	for _, e := range backlog {
		if err := writeEvent(w, e.ID, e.Type, e); err != nil {
			return
		}
	}
	flusher.Flush()

	every := s.Heartbeat
	if every <= 0 {
		every = 15 * time.Second
	}
	beat := time.NewTicker(every)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if err := writeEvent(w, e.ID, e.Type, e); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, id uint64, typ string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if id > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
	return err
}

func intParam(v string, def, min, max int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("must be a number")
	}
	if n < min || n > max {
		return 0, fmt.Errorf("must be between %d and %d", min, max)
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
