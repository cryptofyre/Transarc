package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStalled is returned when the encoder stops producing new frames.
var ErrStalled = errors.New("encoder stalled")

// Progress is one ffmpeg -progress heartbeat.
type Progress struct {
	Frame    int
	FPS      float64
	Speed    float64
	OutBytes int64
	OutTime  time.Duration
	Done     bool
}

// Runner executes ffmpeg.
type Runner struct {
	Bin        string
	StallAfter time.Duration
	MaxRetries int
	OnProgress func(Progress)
	OnRetry    func(reason string)
}

// Run executes args, stripping unrecognized options and narrowing stream maps.
func (r Runner) Run(ctx context.Context, args []string) error {
	if r.Bin == "" {
		return fmt.Errorf("ffmpeg path is empty")
	}
	retries := r.MaxRetries
	if retries <= 0 {
		retries = 6
	}
	mode := "full"
	current := append([]string(nil), args...)
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		err := r.once(ctx, current)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(context.Cause(ctx), context.Canceled) {
			return err
		}
		if errors.Is(err, ErrStalled) {
			return err
		}
		lastErr = err
		if name := UnrecognizedOption(err.Error()); name != "" {
			next, ok := StripOption(current, name)
			if ok {
				if r.OnRetry != nil {
					r.OnRetry("dropped unsupported option -" + name)
				}
				current = next
				continue
			}
		}
		if next, ok := NextMap(mode); ok && muxRejected(err.Error()) {
			mode = next
			rebuilt, rerr := remap(args, next)
			if rerr != nil {
				return err
			}
			if r.OnRetry != nil {
				r.OnRetry("retrying with stream map " + next)
			}
			current = rebuilt
			continue
		}
		return err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("encode failed")
	}
	return lastErr
}

func remap(original []string, mode string) ([]string, error) {
	// Rebuild is done by the caller when it still has the Request. Here we
	// only swap the map block inside an existing argv, which is the path used
	// after a mux error on the command we were given.
	out := make([]string, 0, len(original)+4)
	skipped := false
	for i := 0; i < len(original); i++ {
		a := original[i]
		if a == "-map" || a == "-map_metadata" || a == "-map_chapters" || a == "-sn" || a == "-dn" {
			if i+1 < len(original) && !strings.HasPrefix(original[i+1], "-") && a != "-sn" && a != "-dn" {
				i++
			}
			skipped = true
			continue
		}
		if skipped && a == "-c" {
			out = append(out, mapArgs(mode)...)
			skipped = false
		}
		out = append(out, a)
	}
	if !skipped && !contains(out, "-map") {
		return nil, fmt.Errorf("could not remap ffmpeg arguments")
	}
	if skipped {
		// maps were removed and -c was never seen; append maps at the end
		// before the output path (last arg).
		if len(out) == 0 {
			return nil, fmt.Errorf("could not remap ffmpeg arguments")
		}
		last := out[len(out)-1]
		out = append(out[:len(out)-1], mapArgs(mode)...)
		out = append(out, last)
	}
	return out, nil
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func muxRejected(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "subtitle codec") ||
		strings.Contains(l, "data stream") ||
		strings.Contains(l, "attachment") ||
		strings.Contains(l, "could not find tag") ||
		strings.Contains(l, "invalid argument") && strings.Contains(l, "subtitle")
}

func (r Runner) once(ctx context.Context, args []string) error {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(runCtx, r.Bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	var stderrBuf bytes.Buffer
	var lastUnix atomic.Int64
	lastUnix.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		r.watchStall(runCtx, cancel, &lastUnix)
	}()
	go func() {
		defer wg.Done()
		r.readProgress(runCtx, &lastUnix, stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&limited{buf: &stderrBuf, max: 48 << 10}, stderr)
	}()
	waitErr := cmd.Wait()
	// Unblock the stall watcher. The first cancel wins, so a stall cause is kept.
	cancel(nil)
	wg.Wait()
	if errors.Is(context.Cause(runCtx), ErrStalled) {
		return fmt.Errorf("%w", ErrStalled)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return fmt.Errorf("ffmpeg: %s", tail(msg, 12))
	}
	return nil
}

func (r Runner) watchStall(ctx context.Context, cancel context.CancelCauseFunc, lastUnix *atomic.Int64) {
	stall := r.StallAfter
	if stall <= 0 {
		stall = 20 * time.Minute
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			last := time.Unix(0, lastUnix.Load())
			if time.Since(last) > stall {
				cancel(ErrStalled)
				return
			}
		}
	}
}

func (r Runner) readProgress(ctx context.Context, lastUnix *atomic.Int64, stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	fields := map[string]string{}
	lastFrame := -1
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := sc.Text()
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[key] = val
		if key != "progress" {
			continue
		}
		p := parseProgress(fields)
		if p.Frame != lastFrame {
			lastFrame = p.Frame
			lastUnix.Store(time.Now().UnixNano())
		}
		if r.OnProgress != nil {
			r.OnProgress(p)
		}
	}
}

func parseProgress(fields map[string]string) Progress {
	p := Progress{}
	p.Frame, _ = strconv.Atoi(fields["frame"])
	p.FPS, _ = strconv.ParseFloat(fields["fps"], 64)
	p.OutBytes, _ = strconv.ParseInt(fields["total_size"], 10, 64)
	if t, err := parseOutTime(fields["out_time"]); err == nil {
		p.OutTime = t
	}
	speed := strings.TrimSuffix(fields["speed"], "x")
	p.Speed, _ = strconv.ParseFloat(speed, 64)
	p.Done = fields["progress"] == "end"
	return p
}

func parseOutTime(s string) (time.Duration, error) {
	// hh:mm:ss.micro
	if s == "" || s == "N/A" {
		return 0, fmt.Errorf("empty")
	}
	hms, frac, _ := strings.Cut(s, ".")
	parts := strings.Split(hms, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad out_time %q", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, fmt.Errorf("bad out_time %q", s)
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second
	if frac != "" {
		if len(frac) > 6 {
			frac = frac[:6]
		}
		for len(frac) < 6 {
			frac += "0"
		}
		us, err := strconv.Atoi(frac)
		if err == nil {
			d += time.Duration(us) * time.Microsecond
		}
	}
	return d, nil
}

func tail(s string, lines int) string {
	all := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var kept []string
	for _, line := range all {
		line = strings.TrimSpace(line)
		if line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) > lines {
		kept = kept[len(kept)-lines:]
	}
	return strings.Join(kept, " | ")
}

type limited struct {
	buf *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.buf.Len() >= l.max {
		return len(p), nil
	}
	remain := l.max - l.buf.Len()
	if len(p) > remain {
		p = p[:remain]
	}
	_, _ = l.buf.Write(p)
	return len(p), nil
}
