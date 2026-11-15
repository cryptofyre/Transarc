package config

import "testing"

func TestMapPathLongestPrefix(t *testing.T) {
	maps := []PathMap{
		{From: "/tvshows", To: "/wrong"},
		{From: "/tvshows/Library", To: "/libraries/tv"},
		{From: "/media/Library", To: "/libraries/movies"},
	}
	got := MapPath(`/tvshows/Library/Show/ep.mkv`, maps)
	if got != "/libraries/tv/Show/ep.mkv" {
		t.Fatalf("tv %q", got)
	}
	got = MapPath("/media/Library/Movie/movie.mkv", maps)
	if got != "/libraries/movies/Movie/movie.mkv" {
		t.Fatalf("movie %q", got)
	}
	if HasPrefixPath("/mnt/tv2/ep.mkv", "/mnt/tv") {
		t.Fatal("prefix boundary")
	}
}

func TestParsePathMap(t *testing.T) {
	got := ParsePathMap(" /tvshows/Library=/libraries/tv, /media/Library=/libraries/movies ")
	if len(got) != 2 || got[0].To != "/libraries/tv" || got[1].From != "/media/Library" {
		t.Fatalf("%+v", got)
	}
	if len(ParsePathMap("nope")) != 0 {
		t.Fatal("pair without '=' was kept")
	}
}
