package arr

import (
	"testing"

	"github.com/cryptofyre/transarc/internal/config"
)

func TestPathMapAndMatch(t *testing.T) {
	c := New("sonarr", config.Arr{
		URL:    "http://127.0.0.1:8989",
		APIKey: "test",
		PathMap: []config.PathMap{{
			From: "/mnt/tv",
			To:   "/libraries/tv",
		}},
	})
	local := c.toLocal("/mnt/tv/Arrested Development")
	if local != "/libraries/tv/Arrested Development" {
		t.Fatalf("mapped to %q", local)
	}
	items := []Item{{
		ID: 7, Title: "Arrested Development", Kind: "sonarr", LocalPath: local,
	}, {
		ID: 9, Title: "Other", Kind: "sonarr", LocalPath: "/libraries/tv/Other Show",
	}}
	got, ok := Match(items, "/libraries/tv/Arrested Development/Arrested Development - S01E01.mkv")
	if !ok || got.ID != 7 {
		t.Fatalf("%+v %v", got, ok)
	}
	if _, ok := Match(items, "/libraries/tv2/Arrested Development/ep.mkv"); ok {
		t.Fatal("prefix boundary failed")
	}
}
