package library

import (
	"os"
	"path/filepath"
	"strings"
)

// Scan returns video files under roots. Paths are absolute.
func Scan(roots []string, exts []string, excludeDirs []string) ([]string, error) {
	extSet := map[string]struct{}{}
	for _, e := range exts {
		e = strings.ToLower(strings.TrimPrefix(e, "."))
		if e != "" {
			extSet[e] = struct{}{}
		}
	}
	excl := map[string]struct{}{}
	for _, d := range excludeDirs {
		excl[strings.ToLower(d)] = struct{}{}
	}
	var out []string
	for _, root := range roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if name != "." && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				if _, skip := excl[strings.ToLower(name)]; skip && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			lower := strings.ToLower(name)
			if strings.Contains(lower, ".transarc.") {
				return nil
			}
			ext := strings.TrimPrefix(filepath.Ext(lower), ".")
			if _, ok := extSet[ext]; !ok {
				return nil
			}
			if isExtraFile(lower) {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func isExtraFile(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	for _, token := range []string{"sample", "trailer"} {
		if base == token || strings.HasSuffix(base, "-"+token) || strings.HasSuffix(base, "."+token) || strings.Contains(base, " "+token) {
			return true
		}
	}
	return false
}
