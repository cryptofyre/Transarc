package config

import (
	"path"
	"strings"
)

// Clean makes a path comparable across the separators Sonarr, Plex, and Jellyfin return.
func Clean(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" {
		return ""
	}
	return path.Clean(p)
}

// HasPrefixPath reports whether full is prefix or a file inside it.
// A prefix of /mnt/tv does not match /mnt/tv2.
func HasPrefixPath(full, prefix string) bool {
	full = Clean(full)
	prefix = Clean(prefix)
	if full == prefix {
		return true
	}
	if prefix == "" || prefix == "." || prefix == "/" {
		return false
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(full+"/", prefix)
}

// MapPath applies the longest matching path_map prefix.
// An empty map returns the cleaned path unchanged.
func MapPath(remote string, maps []PathMap) string {
	remote = Clean(remote)
	best := remote
	bestLen := -1
	for _, m := range maps {
		from := Clean(m.From)
		if from == "" || from == "." || from == "/" || !HasPrefixPath(remote, from) {
			continue
		}
		if len(from) < bestLen {
			continue
		}
		rest := strings.TrimPrefix(remote, strings.TrimRight(from, "/"))
		best = Clean(m.To + rest)
		bestLen = len(from)
	}
	return best
}

// ParsePathMap reads from=to pairs separated by commas.
// The first '=' in a pair splits the two paths.
func ParsePathMap(v string) []PathMap {
	var out []PathMap
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		from, to, ok := strings.Cut(part, "=")
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			continue
		}
		out = append(out, PathMap{From: from, To: to})
	}
	return out
}
