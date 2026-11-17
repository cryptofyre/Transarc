package ffmpeg

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/probe"
)

// Request is one encode.
type Request struct {
	Backend string
	Input   string
	Output  string
	Device  string
	Video   probe.Video
	Seek    float64
	Limit   float64
	// MapMode is full, media, or av. Fallback retries narrow the map.
	MapMode string
	QSV     config.QSV
	NVENC   config.NVENC
	SVT     config.SVT
}

// Build returns an argv without the ffmpeg binary.
func Build(r Request) ([]string, error) {
	if r.Input == "" || r.Output == "" {
		return nil, fmt.Errorf("input and output are required")
	}
	switch r.Backend {
	case "qsv":
		return buildQSV(r)
	case "nvenc":
		return buildNVENC(r)
	case "svt":
		return buildSVT(r)
	default:
		return nil, fmt.Errorf("unknown backend %q", r.Backend)
	}
}

func buildQSV(r Request) ([]string, error) {
	if r.Device == "" {
		return nil, fmt.Errorf("qsv device is required")
	}
	// Pin the render node. With two Arc cards, omitting the path lets the
	// driver open whichever node it finds first. kernel_driver is left unset
	// so an explicit path works on both i915 and xe.
	args := []string{
		"-hide_banner",
		"-nostdin",
		"-y",
		"-fflags", "+genpts",
		"-analyzeduration", "100M",
		"-probesize", "100M",
		"-init_hw_device", "vaapi=va:" + r.Device + ",driver=iHD",
		"-init_hw_device", "qsv=qs@va",
		"-filter_hw_device", "qs",
		"-hwaccel", "qsv",
		"-hwaccel_output_format", "qsv",
		"-extra_hw_frames", strconv.Itoa(r.QSV.LookAheadDepth + 16),
	}
	args = append(args, inputWindow(r)...)
	args = append(args, "-i", r.Input)
	args = append(args, mapArgs(r.MapMode)...)
	// Every video option names the main video stream (v:0). Remuxes often carry cover art as a
	// second, one-frame video stream; given -c:v or -vf it went to the encoder, never produced a
	// packet, and Matroska wrote nothing until the stall watch gave up. Now it is copied.
	args = append(args,
		"-c", "copy",
		"-c:v:0", "av1_qsv",
		"-preset", r.QSV.Preset,
		"-profile:v:0", "main",
		"-global_quality", strconv.Itoa(r.QSV.GlobalQuality),
		"-g", strconv.Itoa(gop(r.Video.FPS, r.QSV.GOPSeconds)),
	)
	if r.QSV.LowPower {
		args = append(args, "-low_power", "1")
	}
	if r.QSV.LookAheadDepth > 0 {
		args = append(args, "-look_ahead_depth", strconv.Itoa(r.QSV.LookAheadDepth))
	}
	if r.QSV.ExtBRC {
		args = append(args, "-extbrc", "1")
	}
	if r.QSV.AdaptiveI {
		args = append(args, "-adaptive_i", "1")
	}
	if r.QSV.AdaptiveB {
		args = append(args, "-adaptive_b", "1")
	}
	if r.QSV.BStrategy {
		args = append(args, "-b_strategy", "1")
	}
	if r.QSV.BFrames > 0 {
		args = append(args, "-bf", strconv.Itoa(r.QSV.BFrames))
	}
	if r.QSV.Refs > 0 {
		args = append(args, "-refs", strconv.Itoa(r.QSV.Refs))
	}
	args = append(args, "-forced_idr", "1")
	if vf := qsvFilter(r); vf != "" {
		args = append(args, "-filter:v:0", vf)
	}
	args = append(args, colorArgs(r.Video)...)
	args = append(args, muxArgs()...)
	args = append(args, r.Output)
	return args, nil
}

func buildNVENC(r Request) ([]string, error) {
	device := r.Device
	if device == "" {
		device = "0"
	}
	args := []string{
		"-hide_banner",
		"-nostdin",
		"-y",
		"-fflags", "+genpts",
		"-analyzeduration", "100M",
		"-probesize", "100M",
		"-hwaccel", "cuda",
		"-hwaccel_device", device,
		"-hwaccel_output_format", "cuda",
	}
	args = append(args, inputWindow(r)...)
	args = append(args, "-i", r.Input)
	args = append(args, mapArgs(r.MapMode)...)
	args = append(args,
		"-c", "copy",
		"-c:v:0", "av1_nvenc",
		"-gpu", device,
		"-preset", r.NVENC.Preset,
		"-tune", r.NVENC.Tune,
		"-rc", "vbr",
		"-cq", strconv.Itoa(r.NVENC.CQ),
		"-b:v:0", "0",
		"-g", strconv.Itoa(gop(r.Video.FPS, r.NVENC.GOPSeconds)),
	)
	if r.NVENC.SpatialAQ {
		args = append(args, "-spatial-aq", "1")
	}
	if r.NVENC.TemporalAQ {
		args = append(args, "-temporal-aq", "1")
	}
	if r.NVENC.AQStrength > 0 {
		args = append(args, "-aq-strength", strconv.Itoa(r.NVENC.AQStrength))
	}
	if r.NVENC.Multipass != "" {
		args = append(args, "-multipass", r.NVENC.Multipass)
	}
	if r.NVENC.LookAhead > 0 {
		args = append(args, "-rc-lookahead", strconv.Itoa(r.NVENC.LookAhead))
	}
	if r.NVENC.BFrames > 0 {
		args = append(args, "-bf", strconv.Itoa(r.NVENC.BFrames))
	}
	if r.NVENC.BRefMode != "" {
		args = append(args, "-b_ref_mode", r.NVENC.BRefMode)
	}
	if r.NVENC.LookaheadLevel > 0 {
		args = append(args, "-lookahead_level", strconv.Itoa(r.NVENC.LookaheadLevel))
	}
	if r.NVENC.TemporalFilter > 0 {
		args = append(args, "-tf_level", strconv.Itoa(r.NVENC.TemporalFilter))
	}
	if vf := nvencFilter(r); vf != "" {
		args = append(args, "-filter:v:0", vf)
	}
	if want10(r.NVENC.BitDepth, r.Video) {
		args = append(args, "-profile:v:0", "main10", "-pix_fmt:v:0", "p010le")
	}
	args = append(args, colorArgs(r.Video)...)
	args = append(args, muxArgs()...)
	args = append(args, r.Output)
	return args, nil
}

func buildSVT(r Request) ([]string, error) {
	args := []string{
		"-hide_banner",
		"-nostdin",
		"-y",
		"-fflags", "+genpts",
		"-analyzeduration", "100M",
		"-probesize", "100M",
	}
	args = append(args, inputWindow(r)...)
	args = append(args, "-i", r.Input)
	args = append(args, mapArgs(r.MapMode)...)
	args = append(args,
		"-c", "copy",
		"-c:v:0", "libsvtav1",
		"-preset", strconv.Itoa(r.SVT.Preset),
		"-crf", strconv.Itoa(r.SVT.CRF),
		"-g", strconv.Itoa(gop(r.Video.FPS, r.SVT.GOPSeconds)),
	)
	if want10(r.SVT.BitDepth, r.Video) {
		args = append(args, "-pix_fmt:v:0", "yuv420p10le")
	}
	params := "tune=0"
	if r.Video.HDR || r.Video.HLG {
		params += ":enable-hdr=1"
	}
	args = append(args, "-svtav1-params", params)
	args = append(args, colorArgs(r.Video)...)
	args = append(args, muxArgs()...)
	args = append(args, r.Output)
	return args, nil
}

func inputWindow(r Request) []string {
	var args []string
	if r.Seek > 0 {
		args = append(args, "-ss", strconv.FormatFloat(r.Seek, 'f', 3, 64))
	}
	if r.Limit > 0 {
		args = append(args, "-t", strconv.FormatFloat(r.Limit, 'f', 3, 64))
	}
	return args
}

func mapArgs(mode string) []string {
	switch mode {
	case "media":
		return []string{"-map", "0:v:0", "-map", "0:a?", "-map", "0:s?", "-map_metadata", "0", "-map_chapters", "0"}
	case "av":
		return []string{"-map", "0:v:0", "-map", "0:a?", "-map_metadata", "0", "-map_chapters", "0", "-sn", "-dn"}
	default:
		return []string{"-map", "0", "-map_metadata", "0", "-map_chapters", "0", "-dn"}
	}
}

func muxArgs() []string {
	return []string{
		"-max_muxing_queue_size", "4096",
		"-f", "matroska",
		"-progress", "pipe:1",
		"-nostats",
	}
}

func colorArgs(v probe.Video) []string {
	switch {
	case v.HLG:
		return []string{"-color_primaries", "bt2020", "-color_trc", "arib-std-b67", "-colorspace", "bt2020nc", "-color_range", "tv"}
	case v.HDR10():
		return []string{"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", "-color_range", "tv"}
	default:
		return nil
	}
}

func qsvFilter(r Request) string {
	ten := want10(r.QSV.BitDepth, r.Video)
	inter := r.Video.Interlaced()
	switch {
	case ten && inter:
		return "vpp_qsv=deinterlace=1:format=p010le"
	case ten:
		return "vpp_qsv=format=p010le"
	case inter:
		return "vpp_qsv=deinterlace=1"
	default:
		return ""
	}
}

func nvencFilter(r Request) string {
	ten := want10(r.NVENC.BitDepth, r.Video)
	inter := r.Video.Interlaced()
	switch {
	case ten && inter:
		return "yadif_cuda,scale_cuda=format=p010le"
	case ten:
		return "scale_cuda=format=p010le"
	case inter:
		return "yadif_cuda"
	default:
		return ""
	}
}

func want10(mode string, v probe.Video) bool {
	switch strings.ToLower(mode) {
	case "8":
		return false
	case "10":
		return true
	default:
		return v.BitDepth >= 10 || v.HDR || v.HLG
	}
}

func gop(fps float64, seconds int) int {
	if fps < 1 || fps > 120 {
		fps = 24
	}
	if seconds <= 0 {
		seconds = 10
	}
	g := int(math.Round(fps * float64(seconds)))
	if g < 24 {
		g = 24
	}
	return g
}

// NextMap returns the next narrower stream map after a mux failure.
func NextMap(mode string) (string, bool) {
	switch mode {
	case "", "full":
		return "media", true
	case "media":
		return "av", true
	default:
		return "", false
	}
}

// StripOption removes a flag and its value. It reports whether anything changed.
func StripOption(args []string, name string) ([]string, bool) {
	flag := "-" + name
	out := make([]string, 0, len(args))
	removed := false
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			removed = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out, removed
}

// UnrecognizedOption reads an FFmpeg error for a flag it refused.
func UnrecognizedOption(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "unrecognized option") || strings.Contains(low, "option not found") {
			if name := quotedOption(line); name != "" {
				return name
			}
		}
		if strings.Contains(low, "error setting option") {
			if name := settingOption(line); name != "" {
				return name
			}
		}
	}
	return ""
}

func settingOption(line string) string {
	const key = "error setting option "
	low := strings.ToLower(line)
	i := strings.Index(low, key)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[i+len(key):])
	name, _, _ := strings.Cut(rest, " ")
	name = strings.Trim(name, ".:'\",")
	if name == "" || strings.HasPrefix(name, "-") {
		return strings.TrimPrefix(name, "-")
	}
	return name
}

func quotedOption(line string) string {
	for _, q := range []string{"'", "\""} {
		i := strings.Index(line, q)
		if i < 0 {
			continue
		}
		rest := line[i+len(q):]
		j := strings.Index(rest, q)
		if j > 0 {
			name := strings.TrimPrefix(rest[:j], "-")
			if name != "" && !strings.ContainsAny(name, " \t") {
				return name
			}
		}
	}
	return ""
}

// FormatArgs renders argv for the log.
func FormatArgs(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, bin)
	for _, a := range args {
		if strings.ContainsAny(a, " \t") {
			parts = append(parts, strconv.Quote(a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// DeviceTestArgs builds a one-second synthetic encode used to prove a GPU opens.
func DeviceTestArgs(backend, device string) ([]string, error) {
	switch backend {
	case "qsv":
		if device == "" {
			return nil, fmt.Errorf("qsv device is required")
		}
		return []string{
			"-hide_banner", "-loglevel", "error",
			"-init_hw_device", "vaapi=va:" + device + ",driver=iHD",
			"-init_hw_device", "qsv=qs@va",
			"-filter_hw_device", "qs",
			"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=1",
			"-c:v", "av1_qsv", "-preset", "veryfast", "-global_quality", "30", "-low_power", "1",
			"-f", "null", "-",
		}, nil
	case "nvenc":
		if device == "" {
			device = "0"
		}
		return []string{
			"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=1",
			"-c:v", "av1_nvenc", "-gpu", device, "-preset", "p1", "-rc", "vbr", "-cq", "32", "-b:v", "0",
			"-f", "null", "-",
		}, nil
	case "svt":
		return []string{
			"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=1",
			"-c:v", "libsvtav1", "-preset", "12", "-crf", "40",
			"-f", "null", "-",
		}, nil
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
}
