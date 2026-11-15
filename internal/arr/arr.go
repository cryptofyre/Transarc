package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
)

// Item is a series or movie folder.
type Item struct {
	ID         int
	Title      string
	Kind       string
	RemotePath string
	LocalPath  string
}

// Client is one Sonarr or Radarr instance.
type Client struct {
	Kind   string
	Base   string
	APIKey string
	Map    []config.PathMap
	HTTP   *http.Client
}

// New builds a client. An empty URL returns nil.
func New(kind string, cfg config.Arr) *Client {
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	return &Client{
		Kind:   kind,
		Base:   strings.TrimRight(cfg.URL, "/"),
		APIKey: cfg.APIKey,
		Map:    cfg.PathMap,
		HTTP:   &http.Client{Timeout: 45 * time.Second},
	}
}

type remoteStats struct {
	EpisodeFileCount int `json:"episodeFileCount"`
	EpisodeCount     int `json:"episodeCount"`
}

type remoteItem struct {
	ID         int         `json:"id"`
	Title      string      `json:"title"`
	Path       string      `json:"path"`
	HasFile    bool        `json:"hasFile"`
	Statistics remoteStats `json:"statistics"`
}

// Stats is the library size behind a series or movie list.
// Files is episode files or movies that have a file. Total is the
// episode count Sonarr uses as the denominator, or the movie count.
type Stats struct {
	Count int
	Files int
	Total int
}

// Items returns library folders translated into this container's paths.
func (c *Client) Items(ctx context.Context) ([]Item, Stats, error) {
	if c == nil {
		return nil, Stats{}, nil
	}
	endpoint := "/api/v3/series"
	if c.Kind == "radarr" {
		endpoint = "/api/v3/movie"
	}
	var raw []remoteItem
	if err := c.get(ctx, endpoint, &raw); err != nil {
		return nil, Stats{}, err
	}
	out := make([]Item, 0, len(raw))
	for _, it := range raw {
		out = append(out, Item{
			ID:         it.ID,
			Title:      it.Title,
			Kind:       c.Kind,
			RemotePath: it.Path,
			LocalPath:  c.toLocal(it.Path),
		})
	}
	return out, summarize(c.Kind, raw), nil
}

func summarize(kind string, raw []remoteItem) Stats {
	st := Stats{Count: len(raw)}
	if kind == "radarr" {
		for _, it := range raw {
			if it.HasFile {
				st.Files++
			}
		}
		st.Total = st.Count
		return st
	}
	for _, it := range raw {
		st.Files += it.Statistics.EpisodeFileCount
		st.Total += it.Statistics.EpisodeCount
	}
	return st
}

// Rescan asks the app to re-read the folder after a file is replaced.
func (c *Client) Rescan(ctx context.Context, id int) error {
	if c == nil {
		return nil
	}
	body := map[string]any{}
	switch c.Kind {
	case "radarr":
		body["name"] = "RescanMovie"
		body["movieId"] = id
	default:
		body["name"] = "RescanSeries"
		body["seriesId"] = id
	}
	return c.post(ctx, "/api/v3/command", body)
}

// Ping checks the API.
func (c *Client) Ping(ctx context.Context) (string, error) {
	if c == nil {
		return "", fmt.Errorf("not configured")
	}
	var st struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := c.get(ctx, "/api/v3/system/status", &st); err != nil {
		return "", err
	}
	return st.AppName + " " + st.Version, nil
}

func (c *Client) toLocal(remote string) string {
	return config.MapPath(remote, c.Map)
}

// Match finds the library item whose folder contains file.
func Match(items []Item, file string) (Item, bool) {
	file = config.Clean(file)
	var found Item
	best := -1
	for _, it := range items {
		local := config.Clean(it.LocalPath)
		if local == "." || local == "/" || local == "" {
			continue
		}
		if config.HasPrefixPath(file, local) && len(local) > best {
			found = it
			best = len(local)
		}
	}
	return found, best >= 0
}

func (c *Client) get(ctx context.Context, endpoint string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.Kind, endpoint, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", c.Kind, endpoint, briefBody(b))
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(b, dest); err != nil {
		return fmt.Errorf("%s %s: %w", c.Kind, endpoint, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, endpoint string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+endpoint, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", c.Kind, endpoint, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", c.Kind, endpoint, briefBody(b))
	}
	return nil
}

func briefBody(b []byte) string {
	var msg struct {
		Message string `json:"message"`
	}
	s := ""
	if json.Unmarshal(b, &msg) == nil && strings.TrimSpace(msg.Message) != "" {
		s = msg.Message
	} else {
		s = string(b)
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 180 {
		s = s[:180] + "..."
	}
	if s == "" {
		return "empty response"
	}
	return s
}
