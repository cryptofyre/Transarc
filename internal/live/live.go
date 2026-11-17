// Package live keeps the running service's in-memory activity: the pass,
// the files being encoded right now, and a short ring of recent events.
// Nothing here is stored. A restart starts with an empty ring.
package live

import (
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Event types.
const (
	PassStart  = "pass.start"
	PassFinish = "pass.finish"
	JobStart   = "job.start"
	JobPhase   = "job.phase"
	JobSample  = "job.sample"
	JobFinish  = "job.finish"
	JobProg    = "job.progress"
	Prescan    = "prescan"
)

// Job phases.
const (
	PhaseSample = "sample"
	PhaseEncode = "encode"
	PhasePaused = "paused"
)

// Job is one file being worked on.
type Job struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	Device      string    `json:"device"`
	Phase       string    `json:"phase"`
	Percent     float64   `json:"percent"`
	FPS         float64   `json:"fps"`
	Speed       float64   `json:"speed"`
	OutBytes    int64     `json:"out_bytes"`
	SourceBytes int64     `json:"source_bytes"`
	Codec       string    `json:"codec,omitempty"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	HDR         string    `json:"hdr,omitempty"`
	SamplePct   *float64  `json:"sample_savings_pct,omitempty"`
	Started     time.Time `json:"started"`
	Updated     time.Time `json:"updated"`

	resume string
}

// Info describes a file when its job starts.
type Info struct {
	Device      string
	SourceBytes int64
	Codec       string
	Width       int
	Height      int
	HDR         string
}

// Result is how a job ended. An empty Outcome means the job was canceled.
type Result struct {
	Outcome     string  `json:"outcome"`
	Reason      string  `json:"reason,omitempty"`
	SourceBytes int64   `json:"source_bytes,omitempty"`
	OutputBytes int64   `json:"output_bytes,omitempty"`
	SavingsPct  float64 `json:"savings_pct,omitempty"`
}

// PassInfo is a pass as it starts.
type PassInfo struct {
	Candidates int      `json:"candidates"`
	SetAside   int      `json:"set_aside"`
	Devices    []string `json:"devices"`
	DryRun     bool     `json:"dry_run"`
}

// PassResult is a pass as it ends.
type PassResult struct {
	Encoded    int   `json:"encoded"`
	Kept       int   `json:"kept"`
	Skipped    int   `json:"skipped"`
	Failed     int   `json:"failed"`
	SavedBytes int64 `json:"saved_bytes"`
}

// PrescanInfo is a finished library count.
type PrescanInfo struct {
	Files      int `json:"files"`
	Candidates int `json:"candidates"`
	Skipped    int `json:"skipped"`
	Settling   int `json:"settling"`
	Failed     int `json:"failed"`
}

// Event is one thing that happened. Progress events have ID 0 and are not replayed.
type Event struct {
	ID   uint64    `json:"id,omitempty"`
	Type string    `json:"type"`
	Time time.Time `json:"time"`
	Path string    `json:"path,omitempty"`
	Name string    `json:"name,omitempty"`
	Data any       `json:"data,omitempty"`
}

// JobEnd is the data of a job.finish event.
type JobEnd struct {
	Result
	Device      string  `json:"device"`
	DurationSec float64 `json:"duration_sec"`
}

// SampleEnd is the data of a job.sample event.
type SampleEnd struct {
	SavingsPct float64 `json:"savings_pct"`
	Worth      bool    `json:"worth"`
}

// PhaseChange is the data of a job.phase event.
type PhaseChange struct {
	Phase string `json:"phase"`
}

// Pass is the current pass, if any.
type Pass struct {
	Running bool       `json:"running"`
	Started *time.Time `json:"started,omitempty"`
	Info    *PassInfo  `json:"info,omitempty"`
}

// Hub fans events out to subscribers. Every method is safe on a nil Hub.
type Hub struct {
	mu       sync.Mutex
	now      func() time.Time
	every    time.Duration
	size     int
	ring     []Event
	seq      uint64
	jobs     map[string]*Job
	lastProg map[string]time.Time
	pass     Pass
	subs     map[*sub]struct{}
}

type sub struct {
	ch chan Event
}

// New returns a hub that keeps the last size events.
func New(size int) *Hub {
	if size < 1 {
		size = 200
	}
	return &Hub{
		now:      time.Now,
		every:    time.Second,
		size:     size,
		jobs:     map[string]*Job{},
		lastProg: map[string]time.Time{},
		subs:     map[*sub]struct{}{},
	}
}

// SetClock replaces the clock and the progress interval. Tests use it.
func (h *Hub) SetClock(now func() time.Time, every time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.now = now
	h.every = every
	h.mu.Unlock()
}

// Subscribe returns events after since, then live events.
// The channel closes when cancel is called or when the subscriber falls behind.
func (h *Hub) Subscribe(since uint64, buffer int) (backlog []Event, ch <-chan Event, cancel func()) {
	if h == nil {
		c := make(chan Event)
		close(c)
		return nil, c, func() {}
	}
	if buffer < 1 {
		buffer = 64
	}
	s := &sub{ch: make(chan Event, buffer)}
	h.mu.Lock()
	if since > 0 {
		for _, e := range h.ring {
			if e.ID > since {
				backlog = append(backlog, e)
			}
		}
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return backlog, s.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subs[s]; ok {
				delete(h.subs, s)
				close(s.ch)
			}
			h.mu.Unlock()
		})
	}
}

// CloseAll ends every subscription.
func (h *Hub) CloseAll() {
	if h == nil {
		return
	}
	h.mu.Lock()
	for s := range h.subs {
		delete(h.subs, s)
		close(s.ch)
	}
	h.mu.Unlock()
}

// Recent returns the stored events, oldest first.
func (h *Hub) Recent() []Event {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.ring...)
}

// Jobs returns the files in progress, oldest first.
func (h *Hub) Jobs() []Job {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Job, 0, len(h.jobs))
	for _, j := range h.jobs {
		c := *j
		c.resume = ""
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Started.Equal(out[j].Started) {
			return out[i].Path < out[j].Path
		}
		return out[i].Started.Before(out[j].Started)
	})
	return out
}

// Pass returns the current pass.
func (h *Hub) Pass() Pass {
	if h == nil {
		return Pass{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.pass
	if p.Info != nil {
		info := *p.Info
		info.Devices = append([]string(nil), info.Devices...)
		p.Info = &info
	}
	return p
}

// PassStarted records the start of a pass.
func (h *Hub) PassStarted(info PassInfo) {
	if h == nil {
		return
	}
	h.mu.Lock()
	now := h.now()
	h.pass = Pass{Running: true, Started: &now, Info: &info}
	h.record(Event{Type: PassStart, Data: info})
	h.mu.Unlock()
}

// PassFinished records the end of a pass.
func (h *Hub) PassFinished(res PassResult) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pass = Pass{}
	h.record(Event{Type: PassFinish, Data: res})
	h.mu.Unlock()
}

// JobStarted records a file as in progress.
func (h *Hub) JobStarted(path string, info Info) {
	if h == nil {
		return
	}
	h.mu.Lock()
	now := h.now()
	j := &Job{
		Path: path, Name: filepath.Base(path), Device: info.Device,
		SourceBytes: info.SourceBytes, Codec: info.Codec, Width: info.Width, Height: info.Height, HDR: info.HDR,
		Started: now, Updated: now,
	}
	h.jobs[path] = j
	delete(h.lastProg, path)
	snap := *j
	h.record(Event{Type: JobStart, Path: path, Name: j.Name, Data: snap})
	h.mu.Unlock()
}

// Phase moves a job to sample or encode. Progress restarts at zero.
func (h *Hub) Phase(path, phase string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	j, ok := h.jobs[path]
	if !ok || j.Phase == phase {
		h.mu.Unlock()
		return
	}
	j.Phase = phase
	j.Percent, j.FPS, j.Speed, j.OutBytes = 0, 0, 0, 0
	j.Updated = h.now()
	h.record(Event{Type: JobPhase, Path: path, Name: j.Name, Data: PhaseChange{Phase: phase}})
	h.mu.Unlock()
}

// Paused marks a job as waiting for playback to stop, or resumes it.
func (h *Hub) Paused(path string, paused bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	j, ok := h.jobs[path]
	if !ok || (j.Phase == PhasePaused) == paused {
		h.mu.Unlock()
		return
	}
	if paused {
		j.resume = j.Phase
		j.Phase = PhasePaused
	} else {
		j.Phase = j.resume
		j.resume = ""
	}
	j.Updated = h.now()
	h.record(Event{Type: JobPhase, Path: path, Name: j.Name, Data: PhaseChange{Phase: j.Phase}})
	h.mu.Unlock()
}

// Sampled records a sample estimate.
func (h *Hub) Sampled(path string, pct float64, worth bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	name := filepath.Base(path)
	if j, ok := h.jobs[path]; ok {
		v := pct
		j.SamplePct = &v
		j.Updated = h.now()
		name = j.Name
	}
	h.record(Event{Type: JobSample, Path: path, Name: name, Data: SampleEnd{SavingsPct: pct, Worth: worth}})
	h.mu.Unlock()
}

// Progress updates a job. It is broadcast at most once per interval and never stored.
func (h *Hub) Progress(path string, pct, fps, speed float64, outBytes int64) {
	if h == nil {
		return
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	h.mu.Lock()
	j, ok := h.jobs[path]
	if !ok {
		h.mu.Unlock()
		return
	}
	now := h.now()
	j.Percent, j.FPS, j.Speed, j.OutBytes, j.Updated = pct, fps, speed, outBytes, now
	if last, seen := h.lastProg[path]; seen && now.Sub(last) < h.every {
		h.mu.Unlock()
		return
	}
	h.lastProg[path] = now
	snap := *j
	snap.resume = ""
	h.deliver(Event{Type: JobProg, Time: now, Path: path, Name: j.Name, Data: snap})
	h.mu.Unlock()
}

// JobFinished removes a job and records how it ended.
func (h *Hub) JobFinished(path string, res Result) {
	if h == nil {
		return
	}
	h.mu.Lock()
	j, ok := h.jobs[path]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.jobs, path)
	delete(h.lastProg, path)
	if res.Outcome == "" {
		res.Outcome = "canceled"
	}
	now := h.now()
	end := JobEnd{Result: res, Device: j.Device, DurationSec: now.Sub(j.Started).Seconds()}
	h.record(Event{Type: JobFinish, Path: path, Name: j.Name, Data: end})
	h.mu.Unlock()
}

// PrescanDone records a finished library count.
func (h *Hub) PrescanDone(info PrescanInfo) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.record(Event{Type: Prescan, Data: info})
	h.mu.Unlock()
}

// record stamps, stores and delivers e. The caller holds mu.
func (h *Hub) record(e Event) {
	h.seq++
	e.ID = h.seq
	e.Time = h.now()
	h.ring = append(h.ring, e)
	if over := len(h.ring) - h.size; over > 0 {
		h.ring = append([]Event(nil), h.ring[over:]...)
	}
	h.deliver(e)
}

// deliver hands e to every subscriber without blocking. The caller holds mu.
// A subscriber whose buffer is full is dropped; it reconnects with
// Last-Event-ID and replays the rest from the ring.
func (h *Hub) deliver(e Event) {
	for s := range h.subs {
		select {
		case s.ch <- e:
		default:
			delete(h.subs, s)
			close(s.ch)
		}
	}
}
