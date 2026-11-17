package live

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestHub(size int) (*Hub, *clock) {
	c := &clock{t: time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)}
	h := New(size)
	h.SetClock(c.now, time.Second)
	return h, c
}

func types(es []Event) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Type
	}
	return out
}

func TestNilHubIsSafe(t *testing.T) {
	var h *Hub
	h.PassStarted(PassInfo{})
	h.JobStarted("/a.mkv", Info{})
	h.Phase("/a.mkv", PhaseEncode)
	h.Paused("/a.mkv", true)
	h.Sampled("/a.mkv", 50, true)
	h.Progress("/a.mkv", 10, 1, 1, 1)
	h.JobFinished("/a.mkv", Result{})
	h.PrescanDone(PrescanInfo{})
	h.PassFinished(PassResult{})
	h.CloseAll()
	if h.Jobs() != nil || h.Recent() != nil || h.Pass().Running {
		t.Fatal("nil hub returned state")
	}
	_, ch, cancel := h.Subscribe(0, 1)
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("nil hub channel open")
	}
}

func TestJobLifecycle(t *testing.T) {
	h, c := newTestHub(50)
	h.PassStarted(PassInfo{Candidates: 2, Devices: []string{"D128"}})
	if !h.Pass().Running {
		t.Fatal("pass not running")
	}
	h.JobStarted("/m/Good Boy.mkv", Info{Device: "D129", SourceBytes: 46e9, Codec: "hevc", Width: 3840, Height: 2160, HDR: "DV P7 HDR"})
	h.Phase("/m/Good Boy.mkv", PhaseSample)
	h.Sampled("/m/Good Boy.mkv", 93.8, true)
	h.Phase("/m/Good Boy.mkv", PhaseEncode)
	c.t = c.t.Add(90 * time.Second)
	h.Progress("/m/Good Boy.mkv", 25.5, 76, 3.2, 1.4e9)
	jobs := h.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("jobs %d", len(jobs))
	}
	j := jobs[0]
	if j.Name != "Good Boy.mkv" || j.Phase != PhaseEncode || j.Percent != 25.5 || j.Device != "D129" || j.SamplePct == nil || *j.SamplePct != 93.8 {
		t.Fatalf("job %+v", j)
	}
	h.Paused("/m/Good Boy.mkv", true)
	if h.Jobs()[0].Phase != PhasePaused {
		t.Fatal("not paused")
	}
	h.Paused("/m/Good Boy.mkv", false)
	if h.Jobs()[0].Phase != PhaseEncode {
		t.Fatal("did not resume to encode")
	}
	c.t = c.t.Add(time.Hour)
	h.JobFinished("/m/Good Boy.mkv", Result{Outcome: "encoded", SourceBytes: 46e9, OutputBytes: 3e9, SavingsPct: 93.4})
	h.PassFinished(PassResult{Encoded: 1, SavedBytes: 43e9})
	if len(h.Jobs()) != 0 || h.Pass().Running {
		t.Fatal("state not cleared")
	}
	got := types(h.Recent())
	want := []string{PassStart, JobStart, JobPhase, JobSample, JobPhase, JobPhase, JobPhase, JobFinish, PassFinish}
	if len(got) != len(want) {
		t.Fatalf("events %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v", got)
		}
	}
	end := h.Recent()[7].Data.(JobEnd)
	if end.Outcome != "encoded" || end.Device != "D129" || end.DurationSec != 3690 {
		t.Fatalf("end %+v", end)
	}
	for i, e := range h.Recent() {
		if e.ID != uint64(i+1) {
			t.Fatalf("ids %v", h.Recent())
		}
	}
}

func TestCanceledJobAndSameDevice(t *testing.T) {
	h, _ := newTestHub(50)
	h.JobStarted("/a.mkv", Info{Device: "cpu"})
	h.JobStarted("/b.mkv", Info{Device: "cpu"})
	if len(h.Jobs()) != 2 {
		t.Fatal("jobs on the same device replaced each other")
	}
	h.JobFinished("/a.mkv", Result{})
	es := h.Recent()
	if end := es[len(es)-1].Data.(JobEnd); end.Outcome != "canceled" {
		t.Fatalf("outcome %q", end.Outcome)
	}
}

func TestProgressIsThrottledAndNotStored(t *testing.T) {
	h, c := newTestHub(50)
	h.JobStarted("/a.mkv", Info{})
	_, ch, cancel := h.Subscribe(0, 16)
	defer cancel()
	h.Progress("/a.mkv", 1, 0, 0, 0)
	h.Progress("/a.mkv", 2, 0, 0, 0)
	c.t = c.t.Add(1100 * time.Millisecond)
	h.Progress("/a.mkv", 3, 0, 0, 0)
	var got []float64
	for len(ch) > 0 {
		e := <-ch
		if e.Type != JobProg || e.ID != 0 {
			t.Fatalf("event %+v", e)
		}
		got = append(got, e.Data.(Job).Percent)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("progress %v", got)
	}
	if h.Jobs()[0].Percent != 3 {
		t.Fatal("job not updated")
	}
	for _, e := range h.Recent() {
		if e.Type == JobProg {
			t.Fatal("progress stored in ring")
		}
	}
}

func TestBacklogRingAndSlowSubscriber(t *testing.T) {
	h, _ := newTestHub(3)
	for i := 0; i < 5; i++ {
		h.PrescanDone(PrescanInfo{Files: i})
	}
	if r := h.Recent(); len(r) != 3 || r[0].ID != 3 {
		t.Fatalf("ring %v", r)
	}
	backlog, _, cancel := h.Subscribe(3, 4)
	cancel()
	if len(backlog) != 2 || backlog[0].ID != 4 {
		t.Fatalf("backlog %v", backlog)
	}

	_, ch, cancel := h.Subscribe(0, 1)
	defer cancel()
	h.PrescanDone(PrescanInfo{})
	h.PrescanDone(PrescanInfo{})
	if _, ok := <-ch; !ok {
		t.Fatal("first event lost")
	}
	if _, ok := <-ch; ok {
		t.Fatal("slow subscriber was not dropped")
	}
	cancel()
}
