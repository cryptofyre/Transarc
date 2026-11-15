package play

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
)

// Status is one media server as seen at startup.
// Live means a later pass will ask it who has a file open.
type Status struct {
	On           bool
	Live         bool
	Name         string
	Label        string
	Version      string
	Playing      int
	Known        bool
	Unreachable  bool
	SessionsDown bool
	Problem      string
}

// Source returns the files a server currently has open, in this container's paths.
type Source interface {
	Name() string
	Files(ctx context.Context) ([]string, error)
}

// Watcher caches open files for a short interval.
// A failed lookup is cached too, so a down server is not hit on every file.
type Watcher struct {
	Sources []Source
	TTL     time.Duration
	Now     func() time.Time
	OnWarn  func(error)

	mu     sync.Mutex
	at     time.Time
	paths  []string
	err    error
	warned string
}

// Connect builds a watcher for every configured server that has a secret.
// A server that does not answer is still watched, so a pass later in the
// night can see a session if it comes back. Connect does not stop the service.
func Connect(ctx context.Context, cfg config.Config, onWarn func(error)) (*Watcher, []Status) {
	type item struct {
		kind, name, label string
		cfg               config.Play
	}
	items := []item{
		{"plex", "Plex", "PLEX", cfg.Plex},
		{"jellyfin", "Jellyfin", "JELLYFIN", cfg.Jellyfin},
	}
	var sources []Source
	var stats []Status
	for _, it := range items {
		if !it.cfg.Active() {
			continue
		}
		if it.cfg.Secret() == "" {
			msg := it.cfg.TokenErr
			if msg == "" {
				msg = it.name + " token is not configured"
			}
			stats = append(stats, Status{
				On: true, Name: it.name, Label: it.label, Unreachable: true, Problem: msg,
			})
			continue
		}
		srv := newServer(it.kind, it.name, it.label, it.cfg)
		st := srv.probe(ctx)
		stats = append(stats, st)
		sources = append(sources, srv)
	}
	if len(sources) == 0 {
		return nil, stats
	}
	return &Watcher{Sources: sources, OnWarn: onWarn}, stats
}

// Playing reports whether path is one of the cached open files.
func (w *Watcher) Playing(ctx context.Context, path string) (bool, error) {
	return w.match(ctx, path, false)
}

// Refresh asks the servers again before answering.
func (w *Watcher) Refresh(ctx context.Context, path string) (bool, error) {
	return w.match(ctx, path, true)
}

func (w *Watcher) match(ctx context.Context, path string, force bool) (bool, error) {
	if w == nil {
		return false, nil
	}
	paths, err := w.files(ctx, force)
	if err != nil {
		return false, err
	}
	for _, open := range paths {
		if samePath(path, open) {
			return true, nil
		}
	}
	return false, nil
}

func (w *Watcher) files(ctx context.Context, force bool) ([]string, error) {
	ttl := w.TTL
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	w.mu.Lock()
	if !force && !w.at.IsZero() && now.Sub(w.at) < ttl {
		paths := append([]string(nil), w.paths...)
		err := w.err
		w.mu.Unlock()
		return paths, err
	}
	w.mu.Unlock()

	paths, err := w.fetch(ctx)
	w.mu.Lock()
	w.at = now
	w.paths = append([]string(nil), paths...)
	w.err = err
	w.mu.Unlock()
	return append([]string(nil), paths...), err
}

func (w *Watcher) fetch(ctx context.Context) ([]string, error) {
	var paths []string
	var errs []error
	ok := 0
	for _, src := range w.Sources {
		got, err := src.Files(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ok++
		paths = append(paths, got...)
	}
	if ok == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(errs) > 0 {
		w.note(errors.Join(errs...))
	} else {
		w.clearNote()
	}
	return paths, nil
}

func (w *Watcher) note(err error) {
	if err == nil || w.OnWarn == nil {
		return
	}
	msg := err.Error()
	w.mu.Lock()
	if w.warned == msg {
		w.mu.Unlock()
		return
	}
	w.warned = msg
	w.mu.Unlock()
	w.OnWarn(err)
}

func (w *Watcher) clearNote() {
	w.mu.Lock()
	w.warned = ""
	w.mu.Unlock()
}

func samePath(a, b string) bool {
	ca := config.Clean(stripFileURL(a))
	cb := config.Clean(stripFileURL(b))
	if ca == "" || cb == "" || ca == "." || cb == "." || ca == "/" || cb == "/" {
		return false
	}
	if ca == cb {
		return true
	}
	ia, ea := os.Stat(a)
	ib, eb := os.Stat(b)
	if ea != nil || eb != nil {
		ia, ea = os.Stat(ca)
		ib, eb = os.Stat(cb)
	}
	return ea == nil && eb == nil && os.SameFile(ia, ib)
}
