package play

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/cryptofyre/transarc/internal/config"
)

type server struct {
	kind  string
	name  string
	label string
	base  string
	token string
	maps  []config.PathMap
	http  *http.Client
}

func newServer(kind, name, label string, cfg config.Play) *server {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" {
		base = "http://127.0.0.1:32400"
		if kind == "jellyfin" {
			base = "http://127.0.0.1:8096"
		}
	}
	return &server{
		kind:  kind,
		name:  name,
		label: label,
		base:  base,
		token: cfg.Secret(),
		maps:  cfg.PathMap,
		http:  &http.Client{Timeout: 8 * time.Second},
	}
}

func (s *server) Name() string { return s.name }

func (s *server) Files(ctx context.Context) ([]string, error) {
	var raw []string
	var err error
	if s.kind == "jellyfin" {
		raw, err = s.jellyfinFiles(ctx)
	} else {
		raw, err = s.plexFiles(ctx)
	}
	if err != nil {
		return nil, redact(s.token, err)
	}
	out := make([]string, 0, len(raw))
	for _, file := range raw {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		mapped := config.MapPath(stripFileURL(file), s.maps)
		if mapped == "" || mapped == "." || mapped == "/" {
			continue
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (s *server) probe(ctx context.Context) Status {
	st := Status{On: true, Name: s.name, Label: s.label, Live: true}
	ver, err := s.version(ctx)
	if err != nil {
		st.Unreachable = true
		st.Problem = redact(s.token, err).Error()
		return st
	}
	st.Version = ver
	files, err := s.Files(ctx)
	if err != nil {
		st.SessionsDown = true
		st.Problem = redact(s.token, err).Error()
		return st
	}
	st.Playing = unique(files)
	st.Known = true
	return st
}

func (s *server) version(ctx context.Context) (string, error) {
	if s.kind == "jellyfin" {
		body, err := s.get(ctx, "/System/Info/Public")
		if err != nil {
			return "", err
		}
		var doc struct {
			Version string `json:"Version"`
		}
		if json.Unmarshal(body, &doc) != nil || strings.TrimSpace(doc.Version) == "" {
			return "", errors.New("jellyfin identity was not understood")
		}
		return doc.Version, nil
	}
	body, err := s.get(ctx, "/identity")
	if err != nil {
		return "", err
	}
	ver, err := plexVersion(body)
	if err != nil {
		return "", err
	}
	if ver == "" {
		return "", errors.New("plex identity was not understood")
	}
	return ver, nil
}

func (s *server) plexFiles(ctx context.Context) ([]string, error) {
	body, err := s.get(ctx, "/status/sessions")
	if err != nil {
		return nil, err
	}
	return plexSessionFiles(body)
}

func (s *server) jellyfinFiles(ctx context.Context) ([]string, error) {
	body, err := s.get(ctx, "/Sessions")
	if err != nil {
		return nil, err
	}
	var sessions []struct {
		NowPlayingItem *struct {
			Path string `json:"Path"`
		} `json:"NowPlayingItem"`
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(body, &sessions); err != nil {
		return nil, errors.New("jellyfin sessions were not understood")
	}
	var files []string
	for _, sess := range sessions {
		// A paused session still has the file open. Idle sessions have no item.
		if sess.NowPlayingItem == nil {
			continue
		}
		files = append(files, sess.NowPlayingItem.Path)
	}
	return files, nil
}

func (s *server) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.kind == "jellyfin" {
		req.Header.Set("X-Emby-Token", s.token)
	} else {
		req.Header.Set("X-Plex-Token", s.token)
		req.Header.Set("X-Plex-Product", "Transarc")
		req.Header.Set("X-Plex-Client-Identifier", "transarc")
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", s.kind, endpoint, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d", s.kind, endpoint, res.StatusCode)
	}
	return body, nil
}

func plexVersion(body []byte) (string, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", errors.New("plex identity was not understood")
	}
	if body[0] == '{' {
		var doc struct {
			MediaContainer struct {
				Version string `json:"version"`
			} `json:"MediaContainer"`
		}
		if json.Unmarshal(body, &doc) != nil {
			return "", errors.New("plex identity was not understood")
		}
		return doc.MediaContainer.Version, nil
	}
	var doc struct {
		XMLName xml.Name `xml:"MediaContainer"`
		Version string   `xml:"version,attr"`
	}
	if xml.Unmarshal(body, &doc) != nil {
		return "", errors.New("plex identity was not understood")
	}
	return doc.Version, nil
}

func plexSessionFiles(body []byte) ([]string, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	if body[0] == '{' || body[0] == '[' {
		var doc struct {
			MediaContainer struct {
				Metadata []struct {
					Media []struct {
						Part []struct {
							File string `json:"file"`
						} `json:"Part"`
					} `json:"Media"`
				} `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if json.Unmarshal(body, &doc) != nil {
			return nil, errors.New("plex sessions were not understood")
		}
		var files []string
		for _, item := range doc.MediaContainer.Metadata {
			for _, media := range item.Media {
				for _, part := range media.Part {
					files = append(files, part.File)
				}
			}
		}
		return files, nil
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	var files []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, errors.New("plex sessions were not understood")
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Part" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "file" {
				files = append(files, attr.Value)
			}
		}
	}
}

func stripFileURL(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "file://")
	if len(p) >= 4 && p[0] == '/' && unicode.IsLetter(rune(p[1])) && p[2] == ':' && (p[3] == '/' || p[3] == '\\') {
		p = p[1:]
	}
	return p
}

func unique(files []string) int {
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		seen[file] = struct{}{}
	}
	return len(seen)
}

func redact(secret string, err error) error {
	if err == nil || len(secret) < 8 {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, secret) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, secret, "…"))
}
