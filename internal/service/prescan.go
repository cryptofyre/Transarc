package service

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cryptofyre/transarc/internal/human"
	"github.com/cryptofyre/transarc/internal/library"
	"github.com/cryptofyre/transarc/internal/live"
	"github.com/cryptofyre/transarc/internal/state"
)

func (r *Runner) libraryPaths(one string) ([]string, error) {
	if one != "" {
		return []string{one}, nil
	}
	var roots []string
	for _, lib := range r.Cfg.Libraries {
		if _, err := os.Stat(lib.Path); err != nil {
			r.Log.Warn("library %s (%s): %v", lib.Name, lib.Path, err)
			continue
		}
		roots = append(roots, lib.Path)
	}
	return library.Scan(roots, r.Cfg.Rules.Extensions, r.Cfg.Rules.ExcludeDirNames)
}

func (r *Runner) runPrescan(ctx context.Context) {
	if r.Store == nil {
		return
	}
	p, err := r.prescan(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Warn("prescan: %v", err)
		}
		return
	}
	if err := r.Store.SavePrescan(p); err != nil {
		r.Log.Warn("prescan save: %v", err)
	}
	changed := !sameScan(r.snapshotScan(), p)
	r.setScan(p)
	r.Live.PrescanDone(live.PrescanInfo{Files: p.Files, Candidates: p.Candidates, Skipped: p.Skipped, Settling: p.Settling, Failed: p.Failed})
	if !changed {
		r.Log.Info("prescan  queue %s  skip %s", comma(p.Candidates), comma(p.Skipped))
		return
	}
	r.Log.Box("PRESCAN", "library", r.formatScan(p))
}

func (r *Runner) prescan(ctx context.Context) (state.Prescan, error) {
	paths, err := r.libraryPaths("")
	if err != nil {
		return state.Prescan{}, err
	}
	n := r.Cfg.Rules.ProbeConcurrency
	if n < 1 {
		n = 4
	}
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var p state.Prescan
	reasons := map[string]int{}
	var done int
	last := time.Now()
	total := len(paths)
	if total > 0 {
		r.Log.Info("prescan  %s files", comma(total))
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			bucket, reason := r.prescanOne(ctx, path)
			mu.Lock()
			done++
			switch bucket {
			case "queue":
				p.Files++
				p.Candidates++
			case "skip":
				p.Files++
				p.Skipped++
				reasons[reason]++
			case "settling":
				p.Files++
				p.Settling++
			case "failed":
				p.Files++
				p.Failed++
			}
			report := false
			var seen, queued, skipped int
			if total > 20 && time.Since(last) >= 15*time.Second {
				last = time.Now()
				report = true
				seen, queued, skipped = done, p.Candidates, p.Skipped
			}
			mu.Unlock()
			if report {
				r.Log.Info("prescan  %s / %s   queue %s   skip %s", comma(seen), comma(total), comma(queued), comma(skipped))
			}
		}(path)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return state.Prescan{}, ctx.Err()
	}
	p.Finished = time.Now()
	for reason, n := range reasons {
		p.Reasons = append(p.Reasons, state.ReasonCount{Reason: reason, N: n})
	}
	sortReasons(p.Reasons)
	return p, nil
}

func (r *Runner) prescanOne(ctx context.Context, path string) (bucket, reason string) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return "", ""
	}
	if r.Cfg.Rules.SettleMinutes > 0 && time.Since(fi.ModTime()) < time.Duration(r.Cfg.Rules.SettleMinutes)*time.Minute {
		return "settling", ""
	}
	if rec, ok, err := r.Store.Get(path); err == nil && ok {
		if kind, why, hit := remembered(rec, fi.Size(), fi.ModTime(), r.Cfg.RetryAfter()); hit {
			return kind, why
		}
	}
	_, skip, ok := r.classify(ctx, path)
	rec, found, _ := r.Store.Get(path)
	if ok {
		return "queue", ""
	}
	if skip {
		if found {
			return "skip", bucketReason(rec.Decision, rec.Reason)
		}
		return "settling", ""
	}
	if found && rec.Decision == "failed" {
		return "failed", ""
	}
	return "failed", ""
}

func remembered(rec state.Record, size int64, mod time.Time, retry time.Duration) (kind, reason string, hit bool) {
	if rec.Size != size || rec.ModUnix != mod.Unix() {
		return "", "", false
	}
	switch rec.Decision {
	case "queued":
		return "queue", "", true
	case "encoded":
		return "skip", "already encoded", true
	case "skipped":
		return "skip", bucketReason(rec.Decision, rec.Reason), true
	case "kept":
		if retry > 0 && time.Since(time.Unix(rec.CheckedUnix, 0)) < retry {
			return "skip", "kept", true
		}
	}
	return "", "", false
}

func bucketReason(decision, reason string) string {
	switch decision {
	case "encoded":
		return "already encoded"
	case "kept":
		return "kept"
	case "failed":
		return "probe failed"
	}
	switch {
	case strings.HasPrefix(reason, "already "):
		return reason
	case strings.Contains(reason, "profile 5"):
		return "Dolby Vision profile 5"
	case reason == "below minimum size", reason == "no video stream":
		return reason
	default:
		if reason == "" {
			return "skipped"
		}
		return reason
	}
}

func sortReasons(rs []state.ReasonCount) {
	for i := 1; i < len(rs); i++ {
		j := i
		for j > 0 && (rs[j].N > rs[j-1].N || (rs[j].N == rs[j-1].N && rs[j].Reason < rs[j-1].Reason)) {
			rs[j], rs[j-1] = rs[j-1], rs[j]
			j--
		}
	}
}

func (r *Runner) setScan(p state.Prescan) {
	r.scanMu.Lock()
	r.scan = p
	r.scanOK = true
	r.scanMu.Unlock()
}

func (r *Runner) snapshot() (state.Prescan, bool) {
	r.scanMu.Lock()
	defer r.scanMu.Unlock()
	return r.scan, r.scanOK
}

func (r *Runner) snapshotScan() state.Prescan {
	p, _ := r.snapshot()
	return p
}

func sameScan(a, b state.Prescan) bool {
	if !rScanOK(a) {
		return false
	}
	if a.Files != b.Files || a.Candidates != b.Candidates || a.Skipped != b.Skipped || a.Settling != b.Settling || a.Failed != b.Failed || len(a.Reasons) != len(b.Reasons) {
		return false
	}
	for i := range a.Reasons {
		if a.Reasons[i] != b.Reasons[i] {
			return false
		}
	}
	return true
}

func rScanOK(p state.Prescan) bool {
	return p.Files != 0 || p.Candidates != 0 || p.Skipped != 0 || len(p.Reasons) != 0
}

func (r *Runner) scanRows() []string {
	p, ok := r.snapshot()
	if !ok {
		return []string{single("QUEUE", "prescan running")}
	}
	return r.formatScan(p)
}

func (r *Runner) formatScan(p state.Prescan) []string {
	when := ""
	if !p.Finished.IsZero() {
		when = human.When(p.Finished.In(r.Cfg.Location()), r.Hour12)
	}
	rows := []string{
		pair("FILES", comma(p.Files), when),
		pair("QUEUE", comma(p.Candidates), ""),
		pair("SKIP", comma(p.Skipped), ""),
	}
	for _, rc := range p.Reasons {
		if rc.N == 0 || rc.Reason == "" {
			continue
		}
		rows = append(rows, pair("", comma(rc.N), rc.Reason))
	}
	if p.Settling > 0 {
		rows = append(rows, pair("SETTLING", comma(p.Settling), "still changing"))
	}
	if p.Failed > 0 {
		rows = append(rows, pair("FAILED", comma(p.Failed), "retried next pass"))
	}
	return rows
}
