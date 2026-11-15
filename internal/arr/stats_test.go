package arr

import "testing"

func TestSummarize(t *testing.T) {
	series := summarize("sonarr", []remoteItem{
		{Statistics: remoteStats{EpisodeFileCount: 8, EpisodeCount: 8}},
		{Statistics: remoteStats{EpisodeFileCount: 8, EpisodeCount: 10}},
	})
	if series.Count != 2 || series.Files != 16 || series.Total != 18 {
		t.Fatalf("series %+v", series)
	}
	movies := summarize("radarr", []remoteItem{
		{HasFile: true},
		{HasFile: false},
		{HasFile: true},
	})
	if movies.Count != 3 || movies.Files != 2 || movies.Total != 3 {
		t.Fatalf("movies %+v", movies)
	}
}
