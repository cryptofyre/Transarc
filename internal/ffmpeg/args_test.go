package ffmpeg

import (
	"strings"
	"testing"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/probe"
)

func TestArcAndNVENCParametersStaySeparate(t *testing.T) {
	video := probe.Video{FPS: 24000.0 / 1001.0, BitDepth: 10, HDR: true, Width: 3840, Height: 2160}
	qsvArgs, err := Build(Request{
		Backend: "qsv",
		Input:   "in.mkv",
		Output:  "out.mkv",
		Device:  "/dev/dri/renderD129",
		Video:   video,
		QSV: config.QSV{
			Preset: "veryslow", GlobalQuality: 23, LookAheadDepth: 40,
			GOPSeconds: 10, BFrames: 7, Refs: 5, LowPower: true, ExtBRC: true,
			AdaptiveI: true, AdaptiveB: true, BStrategy: true, BitDepth: "auto",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	qsv := strings.Join(qsvArgs, "\n")
	for _, want := range []string{
		"av1_qsv",
		"/dev/dri/renderD129",
		"driver=iHD",
		"qsv=qs@va",
		"-global_quality\n23",
		"-preset\nveryslow",
		"-look_ahead_depth\n40",
		"-low_power\n1",
		"vpp_qsv=format=p010le",
		"smpte2084",
	} {
		if !strings.Contains(qsv, want) {
			t.Fatalf("qsv args missing %q\n%s", want, qsv)
		}
	}
	if strings.Contains(qsv, "av1_nvenc") || strings.Contains(qsv, "-cq") {
		t.Fatalf("qsv args picked up NVENC settings\n%s", qsv)
	}

	nvArgs, err := Build(Request{
		Backend: "nvenc",
		Input:   "in.mkv",
		Output:  "out.mkv",
		Device:  "0",
		Video:   video,
		NVENC: config.NVENC{
			Preset: "p7", Tune: "hq", CQ: 28, LookAhead: 32, BFrames: 4,
			AQStrength: 8, SpatialAQ: true, TemporalAQ: true, Multipass: "fullres",
			BRefMode: "middle", GOPSeconds: 10, BitDepth: "auto",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	nv := strings.Join(nvArgs, "\n")
	for _, want := range []string{"av1_nvenc", "-cq\n28", "-preset\np7", "scale_cuda=format=p010le", "-gpu\n0"} {
		if !strings.Contains(nv, want) {
			t.Fatalf("nvenc args missing %q\n%s", want, nv)
		}
	}
	if strings.Contains(nv, "global_quality") || strings.Contains(nv, "av1_qsv") || strings.Contains(nv, "\n23\n") {
		t.Fatalf("nvenc args picked up Arc ICQ settings\n%s", nv)
	}

	svtArgs, err := Build(Request{
		Backend: "svt",
		Input:   "in.mkv",
		Output:  "out.mkv",
		Video:   video,
		SVT:     config.SVT{Preset: 8, CRF: 32, GOPSeconds: 10, BitDepth: "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svt := strings.Join(svtArgs, "\n")
	for _, want := range []string{"libsvtav1", "-crf\n32", "-preset\n8", "yuv420p10le", "enable-hdr=1", "smpte2084"} {
		if !strings.Contains(svt, want) {
			t.Fatalf("svt args missing %q\n%s", want, svt)
		}
	}
	if strings.Contains(svt, "global_quality") || strings.Contains(svt, "av1_qsv") || strings.Contains(svt, "av1_nvenc") || strings.Contains(svt, "-cq") {
		t.Fatalf("svt args picked up a GPU encoder\n%s", svt)
	}
}

func TestStripUnrecognizedOption(t *testing.T) {
	args := []string{"-look_ahead_depth", "40", "-preset", "veryslow", "-i", "a.mkv"}
	next, ok := StripOption(args, "look_ahead_depth")
	if !ok || strings.Join(next, " ") != "-preset veryslow -i a.mkv" {
		t.Fatalf("%v %v", ok, next)
	}
	if got := UnrecognizedOption("Unrecognized option 'extbrc'."); got != "extbrc" {
		t.Fatalf("got %q", got)
	}
	if got := UnrecognizedOption("[av1_qsv] Error setting option refs to value 8."); got != "refs" {
		t.Fatalf("got %q", got)
	}
}

func TestProgressTime(t *testing.T) {
	d, err := parseOutTime("01:02:03.500000")
	if err != nil {
		t.Fatal(err)
	}
	if d.Seconds() < 3723.5-0.01 || d.Seconds() > 3723.5+0.01 {
		t.Fatalf("%s", d)
	}
	p := parseProgress(map[string]string{"frame": "10", "fps": "80.5", "speed": "3.2x", "total_size": "1000", "progress": "continue"})
	if p.Frame != 10 || p.FPS != 80.5 || p.Speed != 3.2 || p.OutBytes != 1000 {
		t.Fatalf("%+v", p)
	}
}

// Cover art is a second, one-frame video stream. Every video option must name the main stream,
// or the cover goes to the encoder and the whole encode stalls.
func TestVideoOptionsNameTheMainStreamOnly(t *testing.T) {
	video := probe.Video{FPS: 24, BitDepth: 10, HDR: true, Width: 3840, Height: 2160}
	for _, req := range []Request{
		{Backend: "qsv", Device: "/dev/dri/renderD128", QSV: config.QSV{Preset: "veryslow", GlobalQuality: 23, BitDepth: "auto"}},
		{Backend: "nvenc", Device: "0", NVENC: config.NVENC{Preset: "p7", Tune: "hq", CQ: 30, BitDepth: "10"}},
		{Backend: "svt", SVT: config.SVT{Preset: 6, CRF: 30, BitDepth: "10"}},
	} {
		req.Input, req.Output, req.Video = "in.mkv", "out.mkv", video
		args, err := Build(req)
		if err != nil {
			t.Fatalf("%s: %v", req.Backend, err)
		}
		for _, a := range args {
			switch a {
			case "-c:v", "-vf", "-profile:v", "-pix_fmt", "-b:v":
				t.Errorf("%s: %s applies to every video stream, cover art included: %v", req.Backend, a, args)
			}
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-c:v:0 ") {
			t.Errorf("%s: no encoder for the main video stream: %v", req.Backend, args)
		}
	}
}
