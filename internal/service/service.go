package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"github.com/cryptofyre/transarc/internal/api"
	"github.com/cryptofyre/transarc/internal/arr"
	"github.com/cryptofyre/transarc/internal/buildinfo"
	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/control"
	"github.com/cryptofyre/transarc/internal/decide"
	"github.com/cryptofyre/transarc/internal/disk"
	"github.com/cryptofyre/transarc/internal/ffmpeg"
	"github.com/cryptofyre/transarc/internal/gpu"
	"github.com/cryptofyre/transarc/internal/human"
	"github.com/cryptofyre/transarc/internal/live"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/probe"
	"github.com/cryptofyre/transarc/internal/replace"
	"github.com/cryptofyre/transarc/internal/state"
)

// Runner executes passes against the configured libraries.
type Runner struct {
	Cfg      config.Config
	Log      *logx.Logger
	Backend  string
	GPUs     []string
	Version  string
	Sonarr   ArrLink
	Radarr   ArrLink
	Store    *state.Store
	Items    []arr.Item
	Clients  map[string]*arr.Client
	Probe    probe.Prober
	FFmpeg   ffmpeg.Runner
	Hour12   bool
	Sessions Sessions
	Players  []PlayLink
	// PlayEvery is how often an in-progress encode rechecks open sessions.
	// Zero uses 15 seconds.
	PlayEvery time.Duration
	// Live receives pass and encode activity for the HTTP API. Nil is allowed.
	Live *live.Hub

	scanMu sync.Mutex
	scan   state.Prescan
	scanOK bool

	playMu     sync.Mutex
	playWarned bool
}

// Sessions reports whether a media server currently has a file open.
type Sessions interface {
	Playing(ctx context.Context, path string) (bool, error)
}

// PlayLink is one Plex or Jellyfin server on the idle panel.
// Live means a pass will ask that server. A token that could not be read is not live.
type PlayLink struct {
	On           bool
	Live         bool
	Name         string
	Label        string
	Version      string
	Playing      int
	Known        bool
	Unreachable  bool
	SessionsDown bool
}

// errWatching means a session has the file open. The encode waits and tries again.
// The wait is not stored.
var errWatching = errors.New("playing")

// ArrLink is the startup status of one Sonarr or Radarr connection.
// Files is episode files or movies that have a file. Total is the episode
// count those files are out of, or the movie count. HasFiles is set when
// the list call returned those numbers.
type ArrLink struct {
	On          bool
	Version     string
	Count       int
	Files       int
	Total       int
	HasFiles    bool
	Noun        string
	Unreachable bool
	ListDown    bool
}

// Options control a single pass.
type Options struct {
	Limit         int
	DryRun        bool
	IgnoreSavings bool
	File          string
	GPU           string
}

// Summary is the result of one pass.
type Summary struct {
	Scanned int
	Encoded int
	Kept    int
	Skipped int
	Failed  int
	Saved   int64
}

// Pass probes the libraries and encodes until the queue is empty or ctx ends.
func (r *Runner) Pass(ctx context.Context, opt Options) (Summary, error) {
	r.playMu.Lock()
	r.playWarned = false
	r.playMu.Unlock()
	if err := r.recover(); err != nil {
		r.Log.Warn("recovery: %v", err)
	}
	cands, skipped, err := r.candidates(ctx, opt)
	if err != nil {
		return Summary{}, err
	}
	var sum Summary
	sum.Scanned = len(cands) + skipped
	sum.Skipped = skipped
	limit := opt.Limit
	if limit == 0 {
		limit = r.Cfg.Rules.MaxFilesPerRun
	}
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	gpus := r.gpus(opt.GPU)
	if len(gpus) == 0 {
		return sum, fmt.Errorf("no encoders selected")
	}
	slot := "gpus"
	if r.Backend == "svt" {
		slot = "workers"
	}
	r.Log.Box("PASS", "daily pass", []string{
		fmt.Sprintf("backend %s   %s", r.Backend, r.qualityLine()),
		fmt.Sprintf("%s %s", slot, strings.Join(shortNames(gpus), ", ")),
		fmt.Sprintf("candidates %d   set aside %d   threshold %.0f%%", len(cands), skipped, r.minSavings(opt)),
		fmt.Sprintf("sample %s   dry-run %t", sampleLabel(r.Cfg), opt.DryRun || r.Cfg.DryRun),
	})
	r.Live.PassStarted(live.PassInfo{Candidates: len(cands), SetAside: skipped, Devices: shortNames(gpus), DryRun: opt.DryRun || r.Cfg.DryRun})
	defer func() {
		r.Live.PassFinished(live.PassResult{Encoded: sum.Encoded, Kept: sum.Kept, Skipped: sum.Skipped, Failed: sum.Failed, SavedBytes: sum.Saved})
	}()
	if len(cands) == 0 {
		return sum, nil
	}
	dry := opt.DryRun || r.Cfg.DryRun
	if dry {
		for _, c := range cands {
			if r.busy(ctx, c.path) {
				r.Log.Info("playing %s, would pause until playback stops", filepath.Base(c.path))
				continue
			}
			r.Log.Info("would encode %s  %s  %s", human.Bytes(c.media.Size), c.media.Video.Codec, filepath.Base(c.path))
		}
		return sum, nil
	}

	jobs := make(chan candidate)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, gpu := range gpus {
		wg.Add(1)
		go func(gpu string) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					kind, saved := r.process(ctx, gpu, job, opt)
					mu.Lock()
					sum.Saved += saved
					switch kind {
					case "encoded":
						sum.Encoded++
					case "kept":
						sum.Kept++
					case "skipped":
						sum.Skipped++
					case "failed":
						sum.Failed++
					}
					mu.Unlock()
				}
			}
		}(gpu)
	}
	go func() {
		defer close(jobs)
		for _, c := range cands {
			select {
			case <-ctx.Done():
				return
			case jobs <- c:
			}
		}
	}()
	wg.Wait()
	r.Log.Box("PASS", "pass finished", []string{
		fmt.Sprintf("encoded %d   kept original %d   failed %d   skipped %d", sum.Encoded, sum.Kept, sum.Failed, sum.Skipped),
		fmt.Sprintf("saved this pass %s", human.Bytes(sum.Saved)),
	})
	return sum, nil
}

type candidate struct {
	path  string
	media probe.Media
	mod   time.Time
	size  int64
}

func (r *Runner) candidates(ctx context.Context, opt Options) ([]candidate, int, error) {
	paths, err := r.libraryPaths(opt.File)
	if err != nil {
		return nil, 0, err
	}
	type got struct {
		c    candidate
		ok   bool
		skip bool
	}
	out := make([]got, len(paths))
	sem := make(chan struct{}, r.Cfg.Rules.ProbeConcurrency)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			c, skip, ok := r.classify(ctx, p)
			out[i] = got{c: c, ok: ok, skip: skip}
		}(i, p)
	}
	wg.Wait()
	var cands []candidate
	skipped := 0
	for _, g := range out {
		if g.skip {
			skipped++
			continue
		}
		if g.ok {
			cands = append(cands, g.c)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].size > cands[j].size })
	return cands, skipped, nil
}

func (r *Runner) classify(ctx context.Context, p string) (candidate, bool, bool) {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return candidate{}, false, false
	}
	if r.Cfg.Rules.SettleMinutes > 0 && time.Since(fi.ModTime()) < time.Duration(r.Cfg.Rules.SettleMinutes)*time.Minute {
		r.Log.Info("settling %s", filepath.Base(p))
		return candidate{}, true, false
	}
	skip, _, err := r.Store.ShouldSkip(p, fi.Size(), fi.ModTime(), r.Cfg.RetryAfter())
	if err != nil {
		r.Log.Warn("state %s: %v", p, err)
	}
	if skip {
		return candidate{}, true, false
	}
	if min := r.Cfg.Rules.MinFileSizeMB << 20; min > 0 && fi.Size() < min {
		_ = r.Store.Put(state.Record{
			Path: p, Size: fi.Size(), ModUnix: fi.ModTime().Unix(),
			Decision: "skipped", Reason: "below minimum size",
		})
		return candidate{}, true, false
	}
	media, err := r.Probe.Inspect(ctx, p)
	if err != nil {
		r.Log.Warn("probe %s: %v", filepath.Base(p), err)
		_ = r.Store.Put(state.Record{Path: p, Size: fi.Size(), ModUnix: fi.ModTime().Unix(), Decision: "failed", Reason: err.Error()})
		return candidate{}, false, false
	}
	if media.Size == 0 {
		media.Size = fi.Size()
	}
	pre := decide.Pre(decide.PreInput{
		Media:                media,
		MinSize:              r.Cfg.Rules.MinFileSizeMB << 20,
		SkipCodecs:           r.Cfg.Rules.SkipCodecs,
		SkipDolbyVisionProf5: r.Cfg.Rules.SkipDolbyVisionP5,
	})
	if pre.Action == decide.Skip {
		if pre.Permanent {
			_ = r.Store.Put(state.Record{
				Path: p, Size: fi.Size(), ModUnix: fi.ModTime().Unix(),
				Decision: "skipped", Reason: pre.Reason, GPU: "",
			})
		}
		return candidate{}, true, false
	}
	_ = r.Store.Put(state.Record{
		Path: p, Size: fi.Size(), ModUnix: fi.ModTime().Unix(),
		Decision: "queued", Reason: "candidate",
	})
	return candidate{path: p, media: media, mod: fi.ModTime(), size: fi.Size()}, false, true
}

func (r *Runner) process(ctx context.Context, gpu string, job candidate, opt Options) (string, int64) {
	if ctx.Err() != nil {
		return "", 0
	}
	name := filepath.Base(job.path)
	// Paused still counts as open. The wait is not stored.
	if err := r.waitIdle(ctx, job.path); err != nil {
		return "", 0
	}
	v := job.media.Video
	detail := fmt.Sprintf("%s %dx%d %s  %s  audio %d  subs %d", v.Codec, v.Width, v.Height, hdrLabel(*v), human.Bytes(job.size), job.media.AudioCount, job.media.SubCount)
	r.Log.Box("FILE", name, []string{detail, "gpu " + shortGPU(gpu)})
	r.Live.JobStarted(job.path, live.Info{Device: shortGPU(gpu), SourceBytes: job.size, Codec: v.Codec, Width: v.Width, Height: v.Height, HDR: hdrLabel(*v)})
	var res live.Result
	defer func() { r.Live.JobFinished(job.path, res) }()
	if v.DolbyVision && !dolbyProfile5(*v) {
		r.Log.Warn("%s: Dolby Vision dynamic metadata is not retained. The HDR10 base layer is what gets encoded.", name)
	}

	minSave := r.minSavings(opt)
	if r.Cfg.Rules.SampleSeconds > 0 && job.media.Duration > float64(r.Cfg.Rules.SampleSeconds)*3 {
		for {
			r.Live.Phase(job.path, live.PhaseSample)
			worth, pct, err := r.sample(ctx, gpu, job, minSave)
			if errors.Is(err, errWatching) {
				if err := r.waitIdle(ctx, job.path); err != nil {
					return "", 0
				}
				continue
			}
			if err == nil {
				r.Live.Sampled(job.path, pct, worth)
			}
			if err != nil {
				r.Log.Warn("sample %s failed, encoding the full file: %v", name, err)
			} else if !worth {
				reason := fmt.Sprintf("sample estimated %.1f%% savings", pct)
				r.Log.Box("KEPT ORIGINAL", name, []string{reason, "threshold " + human.Pct(minSave)})
				_ = r.put(job, "kept", reason, pct, job.size, 0, gpu)
				res = live.Result{Outcome: "kept", Reason: reason, SourceBytes: job.size, SavingsPct: pct}
				return "kept", 0
			} else {
				r.Log.Info("sample %s estimated %s savings, encoding the full file", name, human.Pct(pct))
			}
			break
		}
	}

	if !r.spaceFor(job.path, job.size) {
		r.Log.Warn("not enough free space to encode %s safely", name)
		res = live.Result{Outcome: "skipped", Reason: "not enough free space", SourceBytes: job.size}
		return "skipped", 0
	}
	out := tempFile(r.Cfg.TempDir, name)
	defer os.Remove(out)
	started := time.Now()
	r.Live.Phase(job.path, live.PhaseEncode)
	for {
		err := r.encode(ctx, gpu, job.media, job.path, out, 0, 0)
		if err == nil {
			break
		}
		if errors.Is(err, errWatching) {
			if err := r.waitIdle(ctx, job.path); err != nil {
				return "", 0
			}
			continue
		}
		if errors.Is(err, context.Canceled) {
			return "", 0
		}
		r.Log.Box("FAILED", name, []string{err.Error()})
		_ = r.put(job, "failed", err.Error(), 0, job.size, 0, gpu)
		res = live.Result{Outcome: "failed", Reason: err.Error(), SourceBytes: job.size}
		return "failed", 0
	}
	outMedia, err := r.Probe.Inspect(ctx, out)
	if err != nil {
		r.Log.Box("FAILED", name, []string{"probe output: " + err.Error()})
		_ = r.put(job, "failed", err.Error(), 0, job.size, 0, gpu)
		res = live.Result{Outcome: "failed", Reason: "probe output: " + err.Error(), SourceBytes: job.size}
		return "failed", 0
	}
	if st, err := os.Stat(out); err == nil && outMedia.Size == 0 {
		outMedia.Size = st.Size()
	}
	post := decide.Post(decide.PostInput{
		Source:            job.media,
		Output:            outMedia,
		MinSavingsPct:     minSave,
		DurationTolerance: r.Cfg.Rules.DurationTolerance,
	})
	if post.Action != decide.Proceed {
		r.Log.Box("KEPT ORIGINAL", name, []string{
			post.Reason,
			fmt.Sprintf("%s -> %s", human.Bytes(job.size), human.Bytes(outMedia.Size)),
		})
		_ = r.put(job, "kept", post.Reason, post.SavingsPct, job.size, outMedia.Size, gpu)
		res = live.Result{Outcome: "kept", Reason: post.Reason, SourceBytes: job.size, OutputBytes: outMedia.Size, SavingsPct: post.SavingsPct}
		return "kept", 0
	}
	if err := r.waitIdle(ctx, job.path); err != nil {
		return "", 0
	}
	fi, err := os.Stat(job.path)
	if err != nil || fi.Size() != job.size || !fi.ModTime().Equal(job.mod) {
		r.Log.Warn("%s changed while encoding, leaving the original in place", name)
		res = live.Result{Outcome: "skipped", Reason: "changed while encoding", SourceBytes: job.size}
		return "skipped", 0
	}
	if err := replace.Commit(job.path, out); err != nil {
		r.Log.Box("FAILED", name, []string{"replace: " + err.Error()})
		_ = r.put(job, "failed", err.Error(), post.SavingsPct, job.size, outMedia.Size, gpu)
		res = live.Result{Outcome: "failed", Reason: "replace: " + err.Error(), SourceBytes: job.size, OutputBytes: outMedia.Size}
		return "failed", 0
	}
	newInfo, _ := os.Stat(job.path)
	newSize := outMedia.Size
	newMod := time.Now()
	if newInfo != nil {
		newSize = newInfo.Size()
		newMod = newInfo.ModTime()
	}
	note := r.notify(ctx, job.path)
	_ = r.Store.Put(state.Record{
		Path: job.path, Size: newSize, ModUnix: newMod.Unix(),
		Decision: "encoded", Reason: post.Reason, SavingsPct: post.SavingsPct,
		SourceBytes: job.size, OutputBytes: newSize, GPU: shortGPU(gpu),
	})
	r.Log.Box("KEPT ENCODE", name, []string{
		fmt.Sprintf("%s -> %s   saved %s (%s)", human.Bytes(job.size), human.Bytes(newSize), human.Bytes(post.SavingsBytes), human.Pct(post.SavingsPct)),
		fmt.Sprintf("gpu %s   %s   %s", shortGPU(gpu), human.Duration(time.Since(started)), note),
	})
	saved := job.size - newSize
	if saved < 0 {
		saved = 0
	}
	res = live.Result{Outcome: "encoded", Reason: post.Reason, SourceBytes: job.size, OutputBytes: newSize, SavingsPct: post.SavingsPct}
	return "encoded", saved
}

func (r *Runner) sample(ctx context.Context, gpu string, job candidate, minSave float64) (bool, float64, error) {
	limit := float64(r.Cfg.Rules.SampleSeconds)
	seek := job.media.Duration * r.Cfg.Rules.SampleStartRatio
	if seek+limit > job.media.Duration {
		seek = job.media.Duration - limit - 1
	}
	if seek < 0 {
		seek = 0
	}
	out := tempFile(r.Cfg.TempDir, "sample-"+filepath.Base(job.path))
	defer os.Remove(out)
	if err := r.encode(ctx, gpu, job.media, job.path, out, seek, limit); err != nil {
		return false, 0, err
	}
	sm, err := r.Probe.Inspect(ctx, out)
	if err != nil {
		return false, 0, err
	}
	if st, err := os.Stat(out); err == nil {
		sm.Size = st.Size()
	}
	pct, _ := decide.EstimateSavings(job.size, sm.Size, job.media.Duration, sm.Duration)
	return decide.SampleWorthFull(pct, minSave), pct, nil
}

func (r *Runner) encode(ctx context.Context, gpu string, media probe.Media, in, out string, seek, limit float64) error {
	return r.runWatched(ctx, in, func(ctx context.Context) error {
		return r.encodeNow(ctx, gpu, media, in, out, seek, limit)
	})
}

func (r *Runner) runWatched(ctx context.Context, path string, fn func(context.Context) error) error {
	if r.Sessions == nil {
		return fn(ctx)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go r.watchPath(ctx, cancel, path)
	err := fn(ctx)
	if errors.Is(context.Cause(ctx), errWatching) {
		return errWatching
	}
	return err
}

func (r *Runner) watchPath(ctx context.Context, cancel context.CancelCauseFunc, path string) {
	tick := time.NewTicker(r.playInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if r.busyFresh(ctx, path) {
				cancel(errWatching)
				return
			}
		}
	}
}

func (r *Runner) playInterval() time.Duration {
	if r.PlayEvery > 0 {
		return r.PlayEvery
	}
	return 15 * time.Second
}

func (r *Runner) waitIdle(ctx context.Context, path string) error {
	if r.Sessions == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	if !r.busy(ctx, path) {
		return nil
	}
	name := filepath.Base(path)
	r.Log.Info("playing %s, pausing until playback stops", name)
	r.Live.Paused(path, true)
	tick := time.NewTicker(r.playInterval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if !r.busyFresh(ctx, path) {
				r.Log.Info("%s playback stopped, continuing", name)
				r.Live.Paused(path, false)
				return nil
			}
		}
	}
}

func (r *Runner) busy(ctx context.Context, path string) bool {
	return r.ask(ctx, path, false)
}

func (r *Runner) busyFresh(ctx context.Context, path string) bool {
	return r.ask(ctx, path, true)
}

func (r *Runner) ask(ctx context.Context, path string, fresh bool) bool {
	if r.Sessions == nil {
		return false
	}
	var (
		on  bool
		err error
	)
	if fresh {
		if refresher, ok := r.Sessions.(interface {
			Refresh(context.Context, string) (bool, error)
		}); ok {
			on, err = refresher.Refresh(ctx, path)
		} else {
			on, err = r.Sessions.Playing(ctx, path)
		}
	} else {
		on, err = r.Sessions.Playing(ctx, path)
	}
	if err != nil {
		r.notePlay(err)
		return false
	}
	return on
}

func (r *Runner) notePlay(err error) {
	if err == nil || r.Log == nil {
		return
	}
	r.playMu.Lock()
	defer r.playMu.Unlock()
	if r.playWarned {
		return
	}
	r.playWarned = true
	r.Log.Warn("playback: %v", err)
}

func (r *Runner) encodeNow(ctx context.Context, gpu string, media probe.Media, in, out string, seek, limit float64) error {
	mode := "full"
	if limit > 0 {
		// A sample takes the main video and the audio only. With every stream mapped, a sparse
		// one (PGS subtitles, an attached cover) never reaches the -t limit, so ffmpeg reads on
		// to the end of the file: tens of GB for a 30 s sample of a remux. Subtitles add next
		// to nothing to the size estimate.
		mode = "av"
	}
	req := ffmpeg.Request{
		Backend: r.Backend,
		Input:   in,
		Output:  out,
		Device:  gpu,
		Video:   *media.Video,
		Seek:    seek,
		Limit:   limit,
		MapMode: mode,
		QSV:     r.Cfg.QSV,
		NVENC:   r.Cfg.NVENC,
		SVT:     r.Cfg.SVT,
	}
	args, err := ffmpeg.Build(req)
	if err != nil {
		return err
	}
	r.Log.Info("ffmpeg %s", ffmpeg.FormatArgs(r.FFmpeg.Bin, args))
	total := media.Duration
	if limit > 0 {
		total = limit
	}
	runner := r.FFmpeg
	fps := media.Video.FPS
	started := time.Now()
	runner.OnProgress = func(p ffmpeg.Progress) {
		pct := 0.0
		if total > 0 {
			pct = p.OutTime.Seconds() / total * 100
		}
		// With every stream mapped, ffmpeg's out_time follows the sparsest one (PGS subtitles) and
		// stays N/A for most of the file. Frames still count, so measure by those.
		if p.OutTime <= 0 && p.Frame > 0 && fps > 0 {
			done := float64(p.Frame) / fps
			if total > 0 {
				pct = done / total * 100
			}
			if p.Speed <= 0 {
				if secs := time.Since(started).Seconds(); secs > 0 {
					p.Speed = done / secs
				}
			}
		}
		r.Log.Progress(shortGPU(gpu), filepath.Base(in), pct, p.FPS, p.Speed, p.OutBytes)
		r.Live.Progress(in, pct, p.FPS, p.Speed, p.OutBytes)
	}
	runner.OnRetry = func(reason string) {
		r.Log.Warn("%s: %s", filepath.Base(in), reason)
	}
	return runner.Run(ctx, args)
}

func (r *Runner) notify(ctx context.Context, file string) string {
	item, ok := arr.Match(r.Items, file)
	if !ok {
		return "no Sonarr or Radarr item matched this path"
	}
	client := r.Clients[item.Kind]
	if client == nil {
		return item.Kind + " is not configured"
	}
	if err := client.Rescan(ctx, item.ID); err != nil {
		r.Log.Warn("rescan %s %d: %v", item.Kind, item.ID, err)
		return item.Kind + " rescan failed"
	}
	return item.Kind + " rescan queued for " + item.Title
}

func (r *Runner) put(job candidate, decision, reason string, pct float64, source, output int64, gpu string) error {
	return r.Store.Put(state.Record{
		Path: job.path, Size: job.size, ModUnix: job.mod.Unix(),
		Decision: decision, Reason: reason, SavingsPct: pct,
		SourceBytes: source, OutputBytes: output, GPU: shortGPU(gpu),
	})
}

func (r *Runner) recover() error {
	var first error
	for _, lib := range r.Cfg.Libraries {
		if err := replace.RecoverTree(lib.Path); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *Runner) spaceFor(dest string, need int64) bool {
	if need < 0 {
		need = 0
	}
	tempFree, err := disk.Free(r.Cfg.TempDir)
	if err == nil && tempFree < uint64(need) {
		return false
	}
	destFree, err := disk.Free(dest)
	if err == nil && destFree < uint64(need) {
		return false
	}
	return true
}

func (r *Runner) gpus(one string) []string {
	if one != "" {
		return []string{one}
	}
	return r.GPUs
}

func (r *Runner) minSavings(opt Options) float64 {
	if opt.IgnoreSavings {
		return 0
	}
	return r.Cfg.Rules.MinSavingsPercent
}

func (r *Runner) qualityLine() string {
	switch r.Backend {
	case "nvenc":
		return fmt.Sprintf("NVENC CQ %d (this is not Arc ICQ)  preset %s", r.Cfg.NVENC.CQ, r.Cfg.NVENC.Preset)
	case "svt":
		return fmt.Sprintf("SVT-AV1 CRF %d (this is not Arc ICQ or NVENC CQ)  preset %d", r.Cfg.SVT.CRF, r.Cfg.SVT.Preset)
	default:
		return fmt.Sprintf("Arc QSV ICQ %d  preset %s  lookahead %d", r.Cfg.QSV.GlobalQuality, r.Cfg.QSV.Preset, r.Cfg.QSV.LookAheadDepth)
	}
}

func sampleLabel(cfg config.Config) string {
	if cfg.Rules.SampleSeconds <= 0 {
		return "off"
	}
	return fmt.Sprintf("%ds", cfg.Rules.SampleSeconds)
}

func tempFile(dir, name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if len(base) > 80 {
		base = base[:80]
	}
	return filepath.Join(dir, fmt.Sprintf("%s.%d.transarc.mkv", base, time.Now().UnixNano()))
}

func shortGPU(gpu string) string {
	if gpu == "cpu" {
		return gpu
	}
	base := filepath.Base(gpu)
	base = strings.TrimPrefix(base, "render")
	if base == gpu || base == "." {
		return "nv" + gpu
	}
	return base
}

func shortNames(gpus []string) []string {
	out := make([]string, len(gpus))
	for i, g := range gpus {
		out[i] = shortGPU(g)
	}
	return out
}

func hdrLabel(v probe.Video) string {
	switch {
	case v.DolbyVision && v.HLG:
		return "DV HLG"
	case v.DolbyVision && v.HDR:
		return fmt.Sprintf("DV P%d HDR", v.DVProfile)
	case v.DolbyVision:
		return fmt.Sprintf("DV P%d", v.DVProfile)
	case v.HLG:
		return "HLG"
	case v.HDR:
		return "HDR10"
	default:
		return "SDR"
	}
}

func dolbyProfile5(v probe.Video) bool {
	return v.DVProfile == 5 || (v.DolbyVision && !v.HDR && !v.HLG)
}

// Serve blocks until ctx is canceled. A pass starts on the cron schedule.
// Overlapping ticks are skipped while a pass is still encoding.
func (r *Runner) Serve(ctx context.Context) error {
	var running atomic.Bool
	var wg sync.WaitGroup
	if r.Live == nil {
		r.Live = live.New(256)
	}
	scanCtx, scanCancel := context.WithCancel(ctx)
	defer scanCancel()
	var scanWG sync.WaitGroup
	if r.Store != nil {
		if p, ok, err := r.Store.LoadPrescan(); err != nil {
			r.Log.Warn("prescan: %v", err)
		} else if ok {
			r.setScan(p)
		}
	}
	launch := func() bool {
		if !running.CompareAndSwap(false, true) {
			return false
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer running.Store(false)
			scanCancel()
			scanWG.Wait()
			if _, err := r.Pass(ctx, Options{}); err != nil && !errors.Is(err, context.Canceled) {
				r.Log.Error("pass: %v", err)
			}
			if r.Store != nil {
				if st, err := r.Store.Summary(); err == nil {
					r.Log.Info("library totals  encoded %d  kept %d  failed %d  saved %s", st.Encoded, st.Kept, st.Failed, human.Bytes(st.SavedBytes))
				}
			}
			if ctx.Err() == nil {
				r.runPrescan(ctx)
			}
		}()
		return true
	}
	start := func() {
		if !launch() {
			r.Log.Warn("previous pass is still running, skipping this tick")
		}
	}
	sched := cron.New(cron.WithLocation(r.Cfg.Location()))
	if _, err := sched.AddFunc(r.Cfg.Schedule, start); err != nil {
		return err
	}
	sched.Start()
	next := time.Time{}
	if entries := sched.Entries(); len(entries) > 0 {
		next = entries[0].Next
	}
	if r.Cfg.RunOnStart {
		r.announce(next, true)
		start()
	} else {
		r.announce(next, false)
		scanWG.Add(1)
		go func() {
			defer scanWG.Done()
			r.runPrescan(scanCtx)
		}()
	}
	go r.idlePulse(ctx, sched, &running)
	apiDone := r.startAPI(ctx, sched)
	go func() {
		sock := control.Path(r.Cfg.StateDB)
		err := control.Listen(ctx, sock, func(cmd string) string {
			switch cmd {
			case "start":
				if !launch() {
					return "busy\na pass is already running\n"
				}
				r.Log.Info("pass started from transarc start")
				return "ok\npass started\n"
			case "plan":
				next := time.Time{}
				if entries := sched.Entries(); len(entries) > 0 {
					next = entries[0].Next
				}
				return r.PlanText(next, running.Load())
			default:
				return "unknown\n"
			}
		})
		if err != nil && ctx.Err() == nil {
			r.Log.Warn("command socket: %v", err)
		}
	}()
	<-ctx.Done()
	scanCancel()
	r.Log.Info("shutting down")
	scanWG.Wait()
	stop := sched.Stop()
	<-stop.Done()
	wg.Wait()
	if apiDone != nil {
		<-apiDone
	}
	return nil
}

// startAPI binds the HTTP API when api.listen is set. A bind failure is a
// warning: encoding does not depend on the API.
func (r *Runner) startAPI(ctx context.Context, sched *cron.Cron) <-chan error {
	listen := strings.TrimSpace(r.Cfg.API.Listen)
	if listen == "" {
		return nil
	}
	srv := &api.Server{
		Hub:     r.Live,
		Store:   r.Store,
		Token:   strings.TrimSpace(r.Cfg.API.Token),
		Version: buildinfo.Version,
		Backend: r.Backend,
		Devices: r.apiDevices(),
		Next: func() time.Time {
			if entries := sched.Entries(); len(entries) > 0 {
				return entries[0].Next
			}
			return time.Time{}
		},
		Scan: r.snapshot,
	}
	addr, done, err := srv.Listen(ctx, listen)
	if err != nil {
		r.Log.Warn("api: %v", err)
		return nil
	}
	if srv.Token == "" {
		r.Log.Info("api listening on %s without a token", addr)
	} else {
		r.Log.Info("api listening on %s", addr)
	}
	return done
}

func (r *Runner) apiDevices() []api.Device {
	seen := map[string]bool{}
	var out []api.Device
	for _, g := range r.GPUs {
		if seen[g] {
			continue
		}
		seen[g] = true
		id := gpu.Identify(g)
		node := id.Node
		if node == "" {
			node = g
		}
		out = append(out, api.Device{ID: shortGPU(g), Node: node, Name: id.Name})
	}
	return out
}

// PlanText is the same panel the service prints, for transarc plan.
func (r *Runner) PlanText(next time.Time, running bool) string {
	kind, title, rows := r.panel(next, false)
	if running {
		kind, title = "PASS", "running now"
	}
	var buf bytes.Buffer
	logx.New(&buf).Schedule(kind, title, rows)
	return buf.String()
}

func (r *Runner) announce(next time.Time, starting bool) {
	kind, title, rows := r.panel(next, starting)
	r.Log.Schedule(kind, title, rows)
}

func (r *Runner) panel(next time.Time, starting bool) (string, string, []string) {
	var rows []string
	loc := r.Cfg.Location()
	if !next.IsZero() {
		rows = append(rows, single("NEXT", human.When(next.In(loc), r.Hour12)))
		rows = append(rows, single("IN", human.Remain(time.Until(next))))
	}
	rows = append(rows, r.planRows()...)
	rows = append(rows, logx.Rule)
	rows = append(rows, r.scanRows()...)
	rows = append(rows, logx.Rule)
	rows = append(rows, r.arrRow("SONARR", r.Sonarr))
	rows = append(rows, r.arrRow("RADARR", r.Radarr))
	for _, p := range r.Players {
		if p.On {
			rows = append(rows, r.playRow(p))
		}
	}
	if total, ok := libraryTotal(r.Sonarr, r.Radarr); ok {
		rows = append(rows, pair("TOTAL", "", total))
	}
	if v := ffmpegVersion(r.Version); v != "" {
		rows = append(rows, single("FFMPEG", v))
	}
	rows = append(rows, r.libraryRows()...)
	rows = append(rows, logx.Rule)
	rows = append(rows, r.encodeRows()...)
	if r.Store != nil {
		if st, err := r.Store.Summary(); err == nil {
			rows = append(rows, logx.Rule)
			rows = append(rows, single("HISTORY", fmt.Sprintf("encoded %-5d kept %-5d skipped %-5d failed %-5d saved %s",
				st.Encoded, st.Kept, st.Skipped, st.Failed, human.Bytes(st.SavedBytes))))
		}
	}
	kind, title := "IDLE", "scheduled"
	if starting {
		kind, title = "START", "running now"
	}
	return kind, title, rows
}

func (r *Runner) idlePulse(ctx context.Context, sched *cron.Cron, running *atomic.Bool) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if running.Load() {
				r.Log.Info("pass still encoding")
				continue
			}
			entries := sched.Entries()
			if len(entries) == 0 {
				r.Log.Info("idle   no schedule")
				continue
			}
			next := entries[0].Next
			msg := fmt.Sprintf("idle   next %s   in %s   %s", human.When(next.In(r.Cfg.Location()), r.Hour12), human.Remain(time.Until(next)), r.idlePlan())
			if p, ok := r.snapshot(); ok {
				msg += fmt.Sprintf("   queue %s   skip %s", comma(p.Candidates), comma(p.Skipped))
			}
			r.Log.Info("%s", msg)
		}
	}
}

func (r *Runner) idlePlan() string {
	return fmt.Sprintf("scan %s on %s, replace at ≥ %.0f%%", r.libraryNames(), deviceList(r.GPUs), r.Cfg.Rules.MinSavingsPercent)
}

func (r *Runner) planRows() []string {
	min := r.Cfg.Rules.MinSavingsPercent
	rows := []string{
		pair("PLAN", "scan", r.libraryNames()+", largest first"),
	}
	if r.Cfg.Rules.SampleSeconds > 0 {
		rows = append(rows, pair("", "sample", fmt.Sprintf("%ds, then full encode if the sample estimates ≥ %.0f%%", r.Cfg.Rules.SampleSeconds, min/2)))
	} else {
		rows = append(rows, pair("", "sample", "off, each file is fully encoded"))
	}
	rows = append(rows, pair("", "replace", fmt.Sprintf("≥ %.0f%% smaller, and the duration must match", min)))
	if r.Cfg.Rules.RetryDays > 0 {
		rows = append(rows, pair("", "remember", fmt.Sprintf("a keep for %d days", r.Cfg.Rules.RetryDays)))
	}
	if skip := r.skipList(); skip != "" {
		rows = append(rows, labeled("SKIP", skip)...)
	}
	rows = append(rows, r.playingRows()...)
	if r.Cfg.DryRun {
		rows = append(rows, pair("MODE", "dry run", "encodes nothing"))
	}
	return rows
}

func (r *Runner) playingRows() []string {
	var live []PlayLink
	for _, p := range r.Players {
		if p.Live {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return nil
	}
	rows := make([]string, 0, len(live))
	for i, p := range live {
		label := ""
		if i == 0 {
			label = "PLAY"
		}
		name := p.Name
		if name == "" {
			name = p.Label
		}
		rows = append(rows, pair(label, name, "pauses while the file is open, continues when playback stops"))
	}
	return rows
}

func (r *Runner) skipList() string {
	var parts []string
	if len(r.Cfg.Rules.SkipCodecs) > 0 {
		parts = append(parts, strings.Join(r.Cfg.Rules.SkipCodecs, ", "))
	}
	if r.Cfg.Rules.SkipDolbyVisionP5 {
		parts = append(parts, "Dolby Vision profile 5")
	}
	if r.Cfg.Rules.MinFileSizeMB > 0 {
		parts = append(parts, fmt.Sprintf("under %d MB", r.Cfg.Rules.MinFileSizeMB))
	}
	if r.Cfg.Rules.SettleMinutes > 0 {
		parts = append(parts, fmt.Sprintf("still changing for %d min", r.Cfg.Rules.SettleMinutes))
	}
	return strings.Join(parts, ", ")
}

func (r *Runner) libraryRows() []string {
	if len(r.Cfg.Libraries) == 0 {
		return []string{single("LIBRARY", "none configured")}
	}
	type item struct {
		name, path, state string
	}
	items := make([]item, 0, len(r.Cfg.Libraries))
	pathW := 8
	for _, lib := range r.Cfg.Libraries {
		name := lib.Name
		if name == "" {
			name = lib.Path
		}
		state := "ready"
		if _, err := os.Stat(lib.Path); err != nil {
			state = "missing"
		}
		items = append(items, item{name, lib.Path, state})
		if n := utf8.RuneCountInString(lib.Path); n > pathW {
			pathW = n
		}
	}
	maxPath := boxInner - labelWidth - 1 - fieldWidth - 1 - 1 - len("missing")
	if pathW > maxPath {
		pathW = maxPath
	}
	rows := make([]string, 0, len(items))
	for i, it := range items {
		label := ""
		if i == 0 {
			label = "LIBRARY"
		}
		rows = append(rows, human.Pad(label, labelWidth)+" "+human.Pad(human.Truncate(it.name, fieldWidth), fieldWidth)+" "+human.Pad(human.Truncate(it.path, pathW), pathW)+" "+it.state)
	}
	return rows
}

func (r *Runner) playRow(p PlayLink) string {
	label := p.Label
	if label == "" {
		label = strings.ToUpper(p.Name)
	}
	if p.Unreachable {
		return single(label, "unreachable")
	}
	ver := trimVersion(p.Version)
	if p.SessionsDown || !p.Known {
		if ver == "" {
			return single(label, "sessions unavailable")
		}
		return pair(label, "unavailable", ver)
	}
	count := comma(p.Playing) + " playing"
	if ver == "" {
		return single(label, count)
	}
	// Plex build strings are longer than the 16-rune field.
	return pair(label, count, ver)
}

func (r *Runner) arrRow(label string, st ArrLink) string {
	if !st.On {
		return single(label, "not configured")
	}
	if st.Unreachable {
		return single(label, "unreachable")
	}
	ver := trimVersion(st.Version)
	if st.ListDown {
		if ver == "" {
			return single(label, "list unavailable")
		}
		return pair(label, ver, "list unavailable")
	}
	return pair(label, ver, arrDetail(st))
}

func arrDetail(st ArrLink) string {
	if st.Noun == "series" && (st.Files > 0 || st.Total > 0) {
		if st.Total > 0 {
			return fmt.Sprintf("%s series    %s / %s episodes", comma(st.Count), comma(st.Files), comma(st.Total))
		}
		return fmt.Sprintf("%s series    %s episodes", comma(st.Count), comma(st.Files))
	}
	if st.Noun == "movies" && st.HasFiles && st.Files != st.Count {
		return fmt.Sprintf("%s / %s movies", comma(st.Files), comma(st.Count))
	}
	return fmt.Sprintf("%s %s", comma(st.Count), st.Noun)
}

func libraryTotal(a, b ArrLink) (string, bool) {
	n := 0
	ok := false
	if a.HasFiles {
		n += a.Files
		ok = true
	}
	if b.HasFiles {
		n += b.Files
		ok = true
	}
	if !ok {
		return "", false
	}
	return comma(n) + " files", true
}

func comma(n int) string {
	if n < 0 {
		n = 0
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(s[:lead])
	for i := lead; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func (r *Runner) encodeRows() []string {
	var rows []string
	switch r.Backend {
	case "nvenc":
		rows = []string{
			pair("ENCODE", "NVENC", fmt.Sprintf("CQ %d", r.Cfg.NVENC.CQ)),
			pair("", "preset "+r.Cfg.NVENC.Preset, "NVENC quality scale"),
		}
	case "svt":
		rows = []string{
			pair("ENCODE", "SVT-AV1", fmt.Sprintf("CRF %d", r.Cfg.SVT.CRF)),
			pair("", fmt.Sprintf("preset %d", r.Cfg.SVT.Preset), "SVT quality scale"),
		}
	default:
		rows = []string{
			pair("ENCODE", "Arc QSV", fmt.Sprintf("ICQ %d", r.Cfg.QSV.GlobalQuality)),
			pair("", "preset "+r.Cfg.QSV.Preset, fmt.Sprintf("lookahead %d", r.Cfg.QSV.LookAheadDepth)),
		}
	}
	return append(rows, r.deviceRows()...)
}

func (r *Runner) deviceRows() []string {
	if len(r.GPUs) == 0 {
		return []string{single("DEVICES", "none")}
	}
	rows := make([]string, 0, len(r.GPUs))
	for i, g := range r.GPUs {
		label := ""
		if i == 0 {
			label = "DEVICES"
		}
		id := gpu.Identify(g)
		node := id.Node
		if node == "" {
			node = g
		}
		detail := deviceDetail(id)
		if detail == "" {
			rows = append(rows, single(label, node))
			continue
		}
		rows = append(rows, pair(label, node, detail))
	}
	return rows
}

func deviceDetail(id gpu.Identity) string {
	var parts []string
	if id.Name != "" {
		parts = append(parts, id.Name)
	}
	if id.PCI != "" {
		parts = append(parts, id.PCI)
	}
	if id.ID != "" {
		parts = append(parts, id.ID)
	}
	return strings.Join(parts, "  ")
}

// Column widths keep every panel row on the same grid. boxInner matches logx.BoxWidth.
const (
	labelWidth = 8
	fieldWidth = 16
	boxInner   = logx.BoxWidth - 4
)

func pair(label, a, b string) string {
	bWidth := boxInner - labelWidth - 1 - fieldWidth - 1
	return human.Pad(label, labelWidth) + " " + human.Pad(a, fieldWidth) + " " + human.Truncate(b, bWidth)
}

func single(label, value string) string {
	return human.Pad(label, labelWidth) + " " + human.Truncate(value, boxInner-labelWidth-1)
}

func labeled(label, value string) []string {
	width := boxInner - labelWidth - 1
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return []string{single(label, "")}
	}
	var rows []string
	var cur []string
	curLen := 0
	flush := func() {
		rows = append(rows, single(label, strings.Join(cur, " ")))
		label = ""
		cur = nil
		curLen = 0
	}
	for _, field := range fields {
		add := utf8.RuneCountInString(field)
		if len(cur) > 0 {
			add++
		}
		if len(cur) > 0 && curLen+add > width {
			flush()
			add = utf8.RuneCountInString(field)
		}
		cur = append(cur, field)
		curLen += add
	}
	if len(cur) > 0 {
		flush()
	}
	return rows
}

func trimVersion(ver string) string {
	ver = strings.TrimSpace(ver)
	if i := strings.IndexByte(ver, ' '); i > 0 {
		rest := strings.TrimSpace(ver[i+1:])
		if rest != "" && rest[0] >= '0' && rest[0] <= '9' {
			return rest
		}
	}
	return ver
}

func (r *Runner) libraryNames() string {
	if len(r.Cfg.Libraries) == 0 {
		return "no libraries"
	}
	names := make([]string, 0, len(r.Cfg.Libraries))
	for _, lib := range r.Cfg.Libraries {
		if lib.Name != "" {
			names = append(names, lib.Name)
			continue
		}
		names = append(names, lib.Path)
	}
	return strings.Join(names, " and ")
}

func deviceList(gpus []string) string {
	if len(gpus) == 0 {
		return "no devices"
	}
	names := make([]string, len(gpus))
	for i, g := range gpus {
		if g == "cpu" {
			names[i] = "cpu"
			continue
		}
		names[i] = filepath.Base(g)
	}
	return strings.Join(names, ", ")
}

func ffmpegVersion(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, " Copyright"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	v = strings.TrimPrefix(v, "ffmpeg version ")
	return strings.TrimSpace(v)
}
