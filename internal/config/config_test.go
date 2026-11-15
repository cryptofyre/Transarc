package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleLoadsAndKeepsSeparateQualityScales(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(Example), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.QSV.GlobalQuality != 23 {
		t.Fatalf("icq %d", c.QSV.GlobalQuality)
	}
	if c.NVENC.CQ != 28 {
		t.Fatalf("cq %d", c.NVENC.CQ)
	}
	if c.QSV.GlobalQuality == c.NVENC.CQ || c.SVT.CRF == c.QSV.GlobalQuality || c.SVT.CRF == c.NVENC.CQ {
		t.Fatal("quality scales were collapsed to the same number")
	}
	if c.SVT.Preset != 8 || c.SVT.CRF != 32 || c.SVT.Jobs != 1 {
		t.Fatalf("svt defaults %+v", c.SVT)
	}
	if !c.QSV.LowPower || !c.Rules.SkipDolbyVisionP5 {
		t.Fatalf("defaults not applied: %+v %+v", c.QSV, c.Rules)
	}
	if c.Schedule != "0 3 * * *" || c.Timezone != "America/Chicago" {
		t.Fatalf("schedule %s tz %s", c.Schedule, c.Timezone)
	}
	if c.Plex.Active() || c.Jellyfin.Active() {
		t.Fatal("empty playback blocks were treated as configured")
	}
}

func TestPlayEnv(t *testing.T) {
	t.Setenv("TRANSARC_PLEX_URL", "http://10.10.70.138:32400")
	t.Setenv("TRANSARC_PLEX_CONFIG", "/plex/Preferences.xml")
	t.Setenv("TRANSARC_PLEX_PATH_MAP", "/tvshows/Library=/libraries/tv,/media/Library=/libraries/movies")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("schedule: \"0 3 * * *\"\ntimezone: America/Chicago\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Plex.URL != "http://10.10.70.138:32400" || c.Plex.Preferences != "/plex/Preferences.xml" {
		t.Fatalf("%+v", c.Plex)
	}
	got := MapPath("/media/Library/Movie/movie.mkv", c.Plex.PathMap)
	if got != "/libraries/movies/Movie/movie.mkv" {
		t.Fatalf("mapped %q", got)
	}
	if c.Jellyfin.Active() {
		t.Fatal("jellyfin was enabled")
	}
}
