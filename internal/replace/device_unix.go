//go:build !windows

package replace

import (
	"os"
	"syscall"
)

func sameDevice(path, dir string) (bool, error) {
	a, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	b, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	as, ok1 := a.Sys().(*syscall.Stat_t)
	bs, ok2 := b.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false, nil
	}
	return as.Dev == bs.Dev, nil
}
