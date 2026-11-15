//go:build linux

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/cryptofyre/transarc/internal/config"
)

// Prepare creates work directories and, when started as root, drops to PUID:PGID
// while keeping the GPU device group.
func Prepare(cfg config.Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.StateDB), 0o755); err != nil && filepath.Dir(cfg.StateDB) != "." {
		return err
	}
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil && filepath.Dir(cfg.LogFile) != "." {
			return err
		}
	}
	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		return err
	}
	ensureFFmpegEnv(cfg.FFmpeg.Path)
	syscall.Umask(0o002)
	if os.Geteuid() != 0 || cfg.PUID <= 0 {
		return nil
	}
	gid := cfg.PGID
	if gid <= 0 {
		gid = cfg.PUID
	}
	for _, dir := range []string{filepath.Dir(cfg.StateDB), cfg.TempDir} {
		if dir == "" || dir == "." {
			continue
		}
		if err := chownTree(dir, cfg.PUID, gid); err != nil {
			return err
		}
	}
	extra := deviceGroups(cfg.Encoder.GPUs)
	if err := syscall.Setgroups(extra); err != nil {
		return fmt.Errorf("set groups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid %d: %w", gid, err)
	}
	if err := syscall.Setuid(cfg.PUID); err != nil {
		return fmt.Errorf("setuid %d: %w", cfg.PUID, err)
	}
	return nil
}

func deviceGroups(gpus []string) []int {
	seen := map[int]struct{}{}
	var out []int
	add := func(path string) {
		fi, err := os.Stat(path)
		if err != nil {
			return
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return
		}
		gid := int(st.Gid)
		if gid == 0 {
			return
		}
		if _, ok := seen[gid]; ok {
			return
		}
		seen[gid] = struct{}{}
		out = append(out, gid)
	}
	for _, g := range gpus {
		if strings.HasPrefix(g, "/dev/") {
			add(g)
		}
	}
	entries, _ := os.ReadDir("/dev/dri")
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			add(filepath.Join("/dev/dri", e.Name()))
		}
	}
	return out
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", path, err)
		}
		return nil
	})
}

func ensureFFmpegEnv(bin string) {
	dir := filepath.Dir(bin)
	if !strings.Contains(dir, "jellyfin-ffmpeg") {
		return
	}
	prepend("PATH", dir)
	prepend("LD_LIBRARY_PATH", dir)
}

func prepend(key, dir string) {
	cur := os.Getenv(key)
	if cur == "" {
		_ = os.Setenv(key, dir)
		return
	}
	if strings.Contains(cur, dir) {
		return
	}
	_ = os.Setenv(key, dir+string(os.PathListSeparator)+cur)
}
