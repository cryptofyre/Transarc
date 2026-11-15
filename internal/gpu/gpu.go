package gpu

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
)

// Choice is the backend and device list for this machine.
type Choice struct {
	Backend  string
	Devices  []string
	Version  string
	Jellyfin bool
}

// Resolve picks a backend. QSV wins when render nodes and av1_qsv exist,
// which is the Unraid Arc case. NVENC is used when an NVIDIA device is
// visible (and on non-Linux test machines). SVT-AV1 is the CPU path for a
// Raspberry Pi or any other host with neither encoder available in hardware.
func Resolve(ctx context.Context, cfg config.Config, bin string) (Choice, error) {
	version, err := output(ctx, bin, "-version")
	if err != nil {
		return Choice{}, fmt.Errorf("ffmpeg %s: %w", bin, err)
	}
	enc, err := output(ctx, bin, "-hide_banner", "-encoders")
	if err != nil {
		return Choice{}, fmt.Errorf("ffmpeg encoders: %w", err)
	}
	c := Choice{
		Version:  firstLine(version),
		Jellyfin: strings.Contains(strings.ToLower(version), "jellyfin"),
	}
	hasQSV := strings.Contains(enc, "av1_qsv")
	hasNV := strings.Contains(enc, "av1_nvenc")
	hasSVT := strings.Contains(enc, "libsvtav1")
	nodes := RenderNodes()
	backend := cfg.Encoder.Backend
	switch backend {
	case "auto":
		switch {
		case len(nodes) > 0 && hasQSV:
			backend = "qsv"
		case hasNV && nvidiaDevice():
			backend = "nvenc"
		case hasSVT:
			backend = "svt"
		case hasNV:
			backend = "nvenc"
		case hasQSV:
			backend = "qsv"
		default:
			return Choice{}, fmt.Errorf("ffmpeg has no av1_qsv, av1_nvenc, or libsvtav1 encoder")
		}
	case "qsv":
		if !hasQSV {
			return Choice{}, fmt.Errorf("ffmpeg has no av1_qsv encoder")
		}
	case "nvenc":
		if !hasNV {
			return Choice{}, fmt.Errorf("ffmpeg has no av1_nvenc encoder")
		}
	case "svt":
		if !hasSVT {
			return Choice{}, fmt.Errorf("ffmpeg has no libsvtav1 encoder")
		}
	}
	if backend == "qsv" && !c.Jellyfin && !cfg.FFmpeg.AllowUpstream {
		return Choice{}, fmt.Errorf("qsv backend requires the Jellyfin FFmpeg build (version string must contain \"Jellyfin\"). Upstream FFmpeg still mishandles Arc in Docker. Set ffmpeg.allow_upstream only for a deliberate test")
	}
	var devices []string
	if backend == "svt" {
		// One file at a time unless asked. SVT already occupies the CPUs.
		n := cfg.SVT.Jobs
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			devices = append(devices, "cpu")
		}
	} else {
		devices = append([]string(nil), cfg.Encoder.GPUs...)
		if len(devices) == 0 {
			if backend == "qsv" {
				devices = nodes
			} else {
				devices = []string{"0"}
			}
		}
	}
	if len(devices) == 0 {
		return Choice{}, fmt.Errorf("no GPU devices found for %s", backend)
	}
	c.Backend = backend
	c.Devices = devices
	return c, nil
}

// nvidiaDevice reports a Linux NVIDIA node. Jellyfin's arm64 build still
// compiles av1_nvenc, so the encoder list alone cannot mean a GPU is present.
// Non-Linux hosts (the RTX laptop) keep the NVENC fallback.
func nvidiaDevice() bool {
	for _, path := range []string{"/dev/nvidia0", "/dev/nvidiactl"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return !linuxHost()
}

func linuxHost() bool {
	_, err := os.Stat("/proc/sys/kernel/ostype")
	return err == nil
}

// RenderNodes lists /dev/dri/renderD* in stable order.
func RenderNodes() []string {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return nil
	}
	var nodes []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			nodes = append(nodes, filepath.Join("/dev/dri", e.Name()))
		}
	}
	sort.Strings(nodes)
	return nodes
}

func output(ctx context.Context, bin string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, msg)
	}
	return buf.String(), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
