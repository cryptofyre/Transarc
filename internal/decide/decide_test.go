package decide

import (
	"testing"

	"github.com/cryptofyre/transarc/internal/probe"
)

func TestPreSkipsAV1AndProfile5(t *testing.T) {
	av1 := Pre(PreInput{Media: probe.Media{Size: 1 << 30, Video: &probe.Video{Codec: "av1"}}, MinSize: 200 << 20, SkipCodecs: []string{"av1"}})
	if av1.Action != Skip || !av1.Permanent {
		t.Fatalf("av1: %+v", av1)
	}
	p5 := Pre(PreInput{
		Media:                probe.Media{Size: 1 << 30, Video: &probe.Video{Codec: "hevc", DolbyVision: true, DVProfile: 5}},
		MinSize:              1,
		SkipDolbyVisionProf5: true,
	})
	if p5.Action != Skip {
		t.Fatalf("profile 5: %+v", p5)
	}
	p7 := Pre(PreInput{
		Media:                probe.Media{Size: 1 << 30, Video: &probe.Video{Codec: "hevc", DolbyVision: true, DVProfile: 7, HDR: true}},
		MinSize:              1,
		SkipDolbyVisionProf5: true,
	})
	if p7.Action != Proceed {
		t.Fatalf("profile 7 should be a candidate: %+v", p7)
	}
}

func TestPostRequiresRealSavingsAndDuration(t *testing.T) {
	src := probe.Media{Size: 1000, Duration: 3600, Video: &probe.Video{Codec: "hevc", HDR: true, BitDepth: 10}}
	small := Post(PostInput{
		Source: src, Output: probe.Media{Size: 800, Duration: 3600, Video: &probe.Video{Codec: "av1", BitDepth: 10}},
		MinSavingsPct: 10, DurationTolerance: 0.01,
	})
	if small.Action != Proceed {
		t.Fatalf("20%% should proceed: %+v", small)
	}
	tiny := Post(PostInput{
		Source: src, Output: probe.Media{Size: 950, Duration: 3600, Video: &probe.Video{Codec: "av1", BitDepth: 10}},
		MinSavingsPct: 10, DurationTolerance: 0.01,
	})
	if tiny.Action != Keep || !tiny.Permanent {
		t.Fatalf("5%% should keep original: %+v", tiny)
	}
	badDur := Post(PostInput{
		Source: src, Output: probe.Media{Size: 400, Duration: 3000, Video: &probe.Video{Codec: "av1", BitDepth: 10}},
		MinSavingsPct: 10, DurationTolerance: 0.01,
	})
	if badDur.Action != Keep {
		t.Fatalf("duration drift should keep original: %+v", badDur)
	}
	crushed := Post(PostInput{
		Source: src, Output: probe.Media{Size: 400, Duration: 3600, Video: &probe.Video{Codec: "av1", BitDepth: 8}},
		MinSavingsPct: 10, DurationTolerance: 0.01,
	})
	if crushed.Action != Keep {
		t.Fatalf("8-bit HDR encode should be rejected: %+v", crushed)
	}
	same := Post(PostInput{
		Source: src, Output: probe.Media{Size: 1000, Duration: 3600, Video: &probe.Video{BitDepth: 10}},
		MinSavingsPct: 0,
	})
	if same.Action != Keep {
		t.Fatalf("same size should keep original: %+v", same)
	}
}

func TestSampleGate(t *testing.T) {
	if SampleWorthFull(-5, 10) {
		t.Fatal("larger sample should not continue")
	}
	if SampleWorthFull(4, 10) {
		t.Fatal("4%% is under half of 10%%")
	}
	if !SampleWorthFull(5, 10) {
		t.Fatal("half the threshold should continue to a full encode")
	}
	pct, est := EstimateSavings(1000, 50, 100, 10)
	if pct < 49 || pct > 51 || est != 500 {
		t.Fatalf("estimate pct=%v bytes=%d", pct, est)
	}
}
