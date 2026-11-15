//go:build windows

package replace

func sameDevice(path, dir string) (bool, error) {
	return false, nil
}
