package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/gpu"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/state"
)

func TestAnnouncePanel(t *testing.T) {
	dir := t.TempDir()
	tv := filepath.Join(dir, "tv")
	if err := os.Mkdir(tv, 0o755); err != nil {
		t.Fatal(err)
	}
	movies := filepath.Join(dir, "movies")
	store, err := state.Open(filepath.Join(dir, "state", "transarc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	sys := filepath.Join(dir, "sys")
	for _, node := range []string{"renderD128", "renderD129"} {
		dev := filepath.Join(sys, node, "device")
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, "vendor"), []byte("0x8086\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, "device"), []byte("0x56a1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := gpu.SysfsDRM
	gpu.SysfsDRM = sys
	t.Cleanup(func() { gpu.SysfsDRM = prev })

	cfg := config.Config{
		Timezone: "America/Chicago",
		Libraries: []config.Library{
			{Name: "tv", Path: tv},
			{Name: "movies", Path: movies},
		},
		Rules: config.Rules{
			MinSavingsPercent: 10,
			SampleSeconds:     30,
			RetryDays:         21,
			SkipCodecs:        []string{"av1"},
			SkipDolbyVisionP5: true,
			MinFileSizeMB:     200,
			SettleMinutes:     15,
		},
		QSV: config.QSV{Preset: "veryslow", GlobalQuality: 23, LookAheadDepth: 40},
	}
	next := time.Date(2026, 11, 15, 3, 0, 0, 0, cfg.Location())
	r := &Runner{
		Cfg:     cfg,
		Backend: "qsv",
		GPUs:    []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		Version: "ffmpeg version 8.1.3-Jellyfin Copyright (c) somebody",
		Sonarr:  ArrLink{On: true, Version: "Sonarr 4.0.20.3014", Count: 97, Files: 3109, Total: 4292, HasFiles: true, Noun: "series"},
		Radarr:  ArrLink{On: true, Version: "Radarr 6.4.4.10685", Count: 533, Files: 266, Total: 533, HasFiles: true, Noun: "movies"},
		Store:   store,
	}
	out := renderPanel(t, r, next, false)
	t.Logf("\n%s", out)

	for _, want := range []string{
		"IDLE",
		"scheduled",
		"Sunday, November 15, 2026 at 03:00 CST",
		"PLAN",
		"tv and movies, largest first",
		"30s, then full encode if the sample estimates ≥ 5%",
		"≥ 10% smaller, and the duration must match",
		"a keep for 21 days",
		"av1",
		"Dolby Vision profile 5",
		"under 200 MB",
		"still changing for 15 min",
		"4.0.20.3014",
		"97 series",
		"3,109 / 4,292 episodes",
		"6.4.4.10685",
		"266 / 533 movies",
		"3,375 files",
		"8.1.3-Jellyfin",
		"ready",
		"missing",
		"Arc QSV",
		"ICQ 23",
		"lookahead 40",
		"renderD128",
		"renderD129",
		"Arc A750",
		"8086:56a1",
		"encoded",
		"saved 0 B",
		"prescan running",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	for _, gone := range []string{
		"The pass scans",
		"nothing encodes until then",
		"Nothing encodes until then.",
		"Sun 15 Nov",
		"6h ",
		"A 30s sample is encoded first.",
		"left alone while open",
		"ffmpeg version",
	} {
		if strings.Contains(out, gone) {
			t.Errorf("panel still has prose %q", gone)
		}
	}
	if fieldCol(out, "6.4.4.10685") != fieldCol(out, "Arc QSV") {
		t.Fatalf("value column drifted\n%s", out)
	}
	detail := fieldCol(out, "ICQ 23")
	for _, needle := range []string{"97 series", "266 / 533 movies", "3,375 files", "Arc A750"} {
		if fieldCol(out, needle) != detail {
			t.Fatalf("detail column drifted on %q\n%s", needle, out)
		}
	}
	if fieldCol(out, "renderD128") != fieldCol(out, "Arc QSV") {
		t.Fatalf("device column drifted\n%s", out)
	}
	if stateCol(out, "ready") != stateCol(out, "missing") {
		t.Fatalf("library state column drifted\n%s", out)
	}

	t.Run("prescan counts", func(t *testing.T) {
		r.setScan(state.Prescan{
			Finished:   time.Date(2026, 11, 14, 21, 40, 0, 0, time.UTC),
			Files:      3375,
			Candidates: 1204,
			Skipped:    2171,
			Reasons: []state.ReasonCount{
				{Reason: "already av1", N: 1900},
				{Reason: "Dolby Vision profile 5", N: 200},
				{Reason: "below minimum size", N: 71},
			},
		})
		got := renderPanel(t, r, next, false)
		for _, want := range []string{
			"1,204",
			"2,171",
			"1,900",
			"already av1",
			"Dolby Vision profile 5",
			"below minimum size",
			"15:40 CST",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q\n%s", want, got)
			}
		}
		if strings.Contains(got, "prescan running") || strings.Contains(got, "nothing encodes") || strings.Contains(got, "sample still decides") || strings.Contains(got, "savings still open") {
			t.Fatalf("stale prescan row\n%s", got)
		}
		if fieldCol(got, "already av1") != fieldCol(got, "ICQ 23") {
			t.Fatalf("prescan detail column drifted\n%s", got)
		}
		if fieldCol(got, "1,204") != fieldCol(got, "Arc QSV") {
			t.Fatalf("prescan count column drifted\n%s", got)
		}
	})

	t.Run("12 hour clock", func(t *testing.T) {
		r.Hour12 = true
		t.Cleanup(func() { r.Hour12 = false })
		got := renderPanel(t, r, next, false)
		if !strings.Contains(got, "Sunday, November 15, 2026 at 3:00 AM CST") {
			t.Fatalf("12-hour next run\n%s", got)
		}
		if strings.Contains(got, "03:00") || strings.Contains(got, "Sun 15 Nov") {
			t.Fatalf("short clock\n%s", got)
		}
		r.Hour12 = false
	})

	t.Run("nvenc", func(t *testing.T) {
		r.Backend = "nvenc"
		r.Cfg.NVENC.CQ = 28
		r.Cfg.NVENC.Preset = "p7"
		got := renderPanel(t, r, next, false)
		if !strings.Contains(got, "CQ 28") || !strings.Contains(got, "NVENC quality scale") || strings.Contains(got, "ICQ") {
			t.Fatalf("nvenc scale\n%s", got)
		}
	})
	t.Run("svt", func(t *testing.T) {
		r.Backend = "svt"
		r.Cfg.SVT.CRF = 32
		r.Cfg.SVT.Preset = 8
		r.GPUs = []string{"cpu"}
		got := renderPanel(t, r, next, false)
		if !strings.Contains(got, "CRF 32") || !strings.Contains(got, "SVT quality scale") || strings.Contains(got, "ICQ") || strings.Contains(got, "CQ") {
			t.Fatalf("svt scale\n%s", got)
		}
	})
	t.Run("start and dry run", func(t *testing.T) {
		r.Backend = "qsv"
		r.GPUs = []string{"/dev/dri/renderD128"}
		r.Cfg.DryRun = true
		r.Cfg.Rules.SampleSeconds = -1
		r.Sonarr = ArrLink{}
		r.Radarr = ArrLink{On: true, Version: "6.4.4.10685", ListDown: true}
		got := renderPanel(t, r, next, true)
		for _, want := range []string{"START", "running now", "off, each file is fully encoded", "dry run", "encodes nothing", "not configured", "list unavailable"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q", want)
			}
		}
		if strings.Contains(got, "0 movies") || strings.Contains(got, "30s, then") {
			t.Fatalf("stale rows\n%s", got)
		}
	})
	t.Run("playback", func(t *testing.T) {
		prev := r.Players
		t.Cleanup(func() { r.Players = prev })
		r.Players = []PlayLink{
			{On: true, Live: true, Name: "Plex", Label: "PLEX", Version: "1.43.5.11079-b3b13e506", Playing: 2, Known: true},
			{On: true, Live: false, Name: "Jellyfin", Label: "JELLYFIN", Unreachable: true},
		}
		got := renderPanel(t, r, next, false)
		for _, want := range []string{"PLAY", "Plex", "pauses while the file is open, continues when playback stops", "1.43.5.11079-b3b13e506", "2 playing", "JELLYFIN", "unreachable"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q\n%s", want, got)
			}
		}
		if strings.Contains(got, "left alone while open") {
			t.Fatalf("old playback row\n%s", got)
		}
	})
}

func renderPanel(t *testing.T, r *Runner, next time.Time, starting bool) string {
	t.Helper()
	var buf bytes.Buffer
	r.Log = logx.New(&buf)
	r.announce(next, starting)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 8 {
		t.Fatalf("short panel:\n%s", out)
	}
	for i, line := range lines {
		if n := utf8.RuneCountInString(line); n != logx.BoxWidth {
			t.Fatalf("line %d width %d, want %d: %q", i, n, logx.BoxWidth, line)
		}
	}
	if !strings.Contains(out, "├") || !strings.Contains(out, "┤") {
		t.Fatalf("missing section rule:\n%s", out)
	}
	return out
}

func fieldCol(out, needle string) int {
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, needle); i >= 0 {
			return utf8.RuneCountInString(line[:i])
		}
	}
	return -1
}

func stateCol(out, word string) int {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, " "+word+" ") && !strings.HasSuffix(strings.TrimRight(line, " │"), word) {
			continue
		}
		if strings.Contains(line, "tv") || strings.Contains(line, "movies") {
			return utf8.RuneCountInString(line[:strings.LastIndex(line, word)])
		}
	}
	return -1
}
