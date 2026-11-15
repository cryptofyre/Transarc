//go:build !linux

package runtime

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/cryptofyre/transarc/internal/config"
)

// Prepare creates work directories. Privilege drop is Linux-only.
func Prepare(cfg config.Config) error {
	if dir := filepath.Dir(cfg.StateDB); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if cfg.LogFile != "" {
		if dir := filepath.Dir(cfg.LogFile); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
	}
	if cfg.TempDir != "" {
		if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
			return err
		}
	}
	dir := filepath.Dir(cfg.FFmpeg.Path)
	if strings.Contains(dir, "jellyfin-ffmpeg") {
		cur := os.Getenv("PATH")
		if !strings.Contains(cur, dir) {
			_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+cur)
		}
	}
	return nil
}
