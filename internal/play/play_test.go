package play

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cryptofyre/transarc/internal/config"
	_ "modernc.org/sqlite"
)

func TestPlexSkipsOpenFileAndKeepsTokenOutOfTheRequest(t *testing.T) {
	const token = "plex-token-value"
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.RawQuery != "" {
			t.Errorf("token leaked into the query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("X-Plex-Token") != token {
			t.Errorf("token header %q", r.Header.Get("X-Plex-Token"))
		}
		switch r.URL.Path {
		case "/identity":
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><MediaContainer machineIdentifier="hidden" version="1.42.2.10156"></MediaContainer>`))
		case "/status/sessions":
			_, _ = w.Write([]byte(`{"MediaContainer":{"size":2,"Metadata":[
				{"type":"episode","Player":{"state":"paused"},"Media":[{"Part":[{"file":"/tvshows/Library/Show/ep.mkv"}]}]},
				{"type":"track","Media":[{"Part":[{"file":"/music/song.mp3"}]}]}
			]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Config{Plex: config.Play{
		URL:   srv.URL,
		Token: token,
		PathMap: []config.PathMap{
			{From: "/tvshows/Library", To: "/libraries/tv"},
		},
	}}
	var warned []string
	watch, stats := Connect(context.Background(), cfg, func(err error) {
		warned = append(warned, err.Error())
	})
	if watch == nil || len(stats) != 1 {
		t.Fatalf("watch %v stats %+v", watch, stats)
	}
	if stats[0].Version != "1.42.2.10156" || stats[0].Playing != 2 || !stats[0].Known || stats[0].Unreachable {
		t.Fatalf("status %+v", stats[0])
	}
	on, err := watch.Playing(context.Background(), "/libraries/tv/Show/ep.mkv")
	if err != nil || !on {
		t.Fatalf("playing %v %v", on, err)
	}
	on, err = watch.Playing(context.Background(), "/libraries/movies/Other.mkv")
	if err != nil || on {
		t.Fatalf("other file %v %v", on, err)
	}
	// identity, the startup session list, and the first Playing lookup.
	if hits != 3 {
		t.Fatalf("cache missed, hits %d", hits)
	}
	if len(warned) != 0 {
		t.Fatalf("warnings %v", warned)
	}
}

func TestPlexXMLSessionsAndIdentityJSON(t *testing.T) {
	ver, err := plexVersion([]byte(`{"MediaContainer":{"version":"1.41.0"}}`))
	if err != nil || ver != "1.41.0" {
		t.Fatalf("json version %q %v", ver, err)
	}
	ver, err = plexVersion([]byte(`<?xml version="1.0"?><MediaContainer version="1.42.2.10156"/>`))
	if err != nil || ver != "1.42.2.10156" {
		t.Fatalf("xml version %q %v", ver, err)
	}
	files, err := plexSessionFiles([]byte(`<MediaContainer size="1"><Video><Media><Part file="/media/Library/Movie.mkv"/></Media></Video></MediaContainer>`))
	if err != nil || len(files) != 1 || files[0] != "/media/Library/Movie.mkv" {
		t.Fatalf("xml files %+v %v", files, err)
	}
}

func TestWatcherRefreshesAndFailOpen(t *testing.T) {
	now := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	var calls int
	src := sourceFunc(func(context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return []string{"/libraries/tv/a.mkv"}, nil
		}
		return nil, errPlain("plex /status/sessions: HTTP 500")
	})
	w := &Watcher{Sources: []Source{src}, TTL: time.Minute, Now: func() time.Time { return now }}
	on, err := w.Playing(context.Background(), "/libraries/tv/a.mkv")
	if err != nil || !on {
		t.Fatalf("first %v %v", on, err)
	}
	on, err = w.Playing(context.Background(), "/libraries/tv/a.mkv")
	if err != nil || !on || calls != 1 {
		t.Fatalf("cached on=%v err=%v calls=%d", on, err, calls)
	}
	now = now.Add(time.Minute)
	on, err = w.Playing(context.Background(), "/libraries/tv/a.mkv")
	if err == nil || on {
		t.Fatalf("refresh should fail open to the caller, on=%v err=%v", on, err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}

func TestSameFileHardLink(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(orig, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.mkv")
	if err := os.Link(orig, link); err != nil {
		t.Skip(err)
	}
	if !samePath(orig, link) {
		t.Fatal("hard link was not the same file")
	}
}

func TestPlexPreferencesTokenStaysOutOfErrors(t *testing.T) {
	const token = "super-secret-token"
	dir := t.TempDir()
	path := filepath.Join(dir, "Preferences.xml")
	body := `<Preferences PlexOnlineToken="` + token + `" FSEventLibraryUpdatesEnabled="1"/>`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tokenFromPlex(path)
	if err != nil || got != token {
		t.Fatalf("token read %q %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`<Preferences PlexOnlineToken="`+token+`"`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = tokenFromPlex(path)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked or was empty: %v", err)
	}
}

func TestJellyfinPrefersNamedKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data", "jellyfin.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURI(dbPath, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE ApiKeys (
		Id INTEGER PRIMARY KEY,
		DateCreated TEXT,
		DateLastActivity TEXT,
		Name TEXT,
		AccessToken TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO ApiKeys (Name, AccessToken, DateLastActivity) VALUES
		('dashboard', 'older-secret-value', '2026-11-14'),
		('Transarc', 'named-secret-value', '2020-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := tokenFromJellyfin(dir)
	if err != nil || got != "named-secret-value" {
		t.Fatalf("key %q %v", got, err)
	}
}

func TestJellyfinPausedSessionCounts(t *testing.T) {
	const key = "jellyfin-api-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != key {
			t.Errorf("key header %q", r.Header.Get("X-Emby-Token"))
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/System/Info/Public":
			_, _ = w.Write([]byte(`{"Version":"10.10.7"}`))
		case "/Sessions":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(key))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := newServer("jellyfin", "Jellyfin", "JELLYFIN", config.Play{URL: srv.URL, APIKey: key})
	_, err := s.Files(context.Background())
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked or was empty: %v", err)
	}

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			_, _ = w.Write([]byte(`{"Version":"10.10.7"}`))
		case "/Sessions":
			_, _ = w.Write([]byte(`[
				{"UserName":"a","PlayState":{"IsPaused":true},"NowPlayingItem":{"Path":"/data/movies/Movie.mkv"}},
				{"UserName":"b","NowPlayingItem":null}
			]`))
		}
	}))
	defer okSrv.Close()
	cfg := config.Config{Jellyfin: config.Play{
		URL:    okSrv.URL,
		APIKey: key,
		PathMap: []config.PathMap{
			{From: "/data/movies", To: "/libraries/movies"},
		},
	}}
	watch, stats := Connect(context.Background(), cfg, nil)
	if len(stats) != 1 || stats[0].Playing != 1 || stats[0].Version != "10.10.7" {
		t.Fatalf("%+v", stats)
	}
	on, err := watch.Playing(context.Background(), "/libraries/movies/Movie.mkv")
	if err != nil || !on {
		t.Fatalf("paused movie %v %v", on, err)
	}
	on, err = watch.Playing(context.Background(), "/libraries/tv/Other.mkv")
	if err != nil || on {
		t.Fatalf("idle session matched %v %v", on, err)
	}
}

func TestPartialFailureWarnsOnce(t *testing.T) {
	w := &Watcher{Sources: []Source{
		sourceFunc(func(context.Context) ([]string, error) {
			return []string{"/libraries/tv/a.mkv"}, nil
		}),
		sourceFunc(func(context.Context) ([]string, error) {
			return nil, errPlain("jellyfin /Sessions: HTTP 500")
		}),
	}, TTL: time.Minute}
	var warns int
	w.OnWarn = func(error) { warns++ }
	on, err := w.Playing(context.Background(), "/libraries/tv/a.mkv")
	if err != nil || !on {
		t.Fatalf("partial %v %v", on, err)
	}
	if _, err := w.Refresh(context.Background(), "/libraries/tv/a.mkv"); err != nil {
		t.Fatal(err)
	}
	if warns != 1 {
		t.Fatalf("warns %d", warns)
	}
}

type sourceFunc func(context.Context) ([]string, error)

func (f sourceFunc) Name() string { return "test" }

func (f sourceFunc) Files(ctx context.Context) ([]string, error) { return f(ctx) }

type errPlain string

func (e errPlain) Error() string { return string(e) }
