package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Media is the part of a file Transarc uses to plan an encode.
type Media struct {
	Path       string
	Format     string
	Duration   float64
	Size       int64
	Video      *Video
	AudioCount int
	SubCount   int
}

// Video describes the first video stream.
type Video struct {
	Codec          string
	Width          int
	Height         int
	PixFmt         string
	BitRate        int64
	FPS            float64
	ColorTransfer  string
	ColorPrimaries string
	ColorSpace     string
	BitDepth       int
	FieldOrder     string
	DolbyVision    bool
	DVProfile      int
	DVCompatID     int
	HDR            bool
	HLG            bool
}

// HDR10 reports static HDR10 signaling, including Dolby Vision profiles that sit on an HDR10 base.
func (v Video) HDR10() bool {
	return strings.EqualFold(v.ColorTransfer, "smpte2084") || v.HDR
}

// Interlaced reports a field order other than progressive.
func (v Video) Interlaced() bool {
	switch strings.ToLower(v.FieldOrder) {
	case "", "progressive", "unknown":
		return false
	default:
		return true
	}
}

// Prober runs ffprobe.
type Prober struct {
	Bin string
}

// Inspect returns media metadata. Missing duration is left at zero.
func (p Prober) Inspect(ctx context.Context, path string) (Media, error) {
	if p.Bin == "" {
		return Media{}, fmt.Errorf("ffprobe path is empty")
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.Bin,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Media{}, fmt.Errorf("ffprobe %s: %s", path, msg)
	}
	return parse(path, out)
}

type raw struct {
	Streams []rawStream `json:"streams"`
	Format  rawFormat   `json:"format"`
}

type rawStream struct {
	CodecType        string            `json:"codec_type"`
	CodecName        string            `json:"codec_name"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	PixFmt           string            `json:"pix_fmt"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	FieldOrder       string            `json:"field_order"`
	AvgFrameRate     string            `json:"avg_frame_rate"`
	RFrameRate       string            `json:"r_frame_rate"`
	ColorTransfer    string            `json:"color_transfer"`
	ColorPrimaries   string            `json:"color_primaries"`
	ColorSpace       string            `json:"color_space"`
	Duration         string            `json:"duration"`
	BitRate          string            `json:"bit_rate"`
	SideData         []rawSide         `json:"side_data_list"`
	Tags             map[string]string `json:"tags"`
}

type rawSide struct {
	SideDataType string `json:"side_data_type"`
	DVProfile    int    `json:"dv_profile"`
	DVCompat     int    `json:"dv_bl_signal_compatibility_id"`
}

type rawFormat struct {
	FormatName string            `json:"format_name"`
	Duration   string            `json:"duration"`
	Size       string            `json:"size"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

func parse(path string, buf []byte) (Media, error) {
	var r raw
	if err := json.Unmarshal(buf, &r); err != nil {
		return Media{}, fmt.Errorf("parse ffprobe: %w", err)
	}
	m := Media{
		Path:     path,
		Format:   r.Format.FormatName,
		Duration: parseFloat(r.Format.Duration),
		Size:     int64(parseFloat(r.Format.Size)),
	}
	for _, s := range r.Streams {
		switch s.CodecType {
		case "audio":
			m.AudioCount++
		case "subtitle":
			m.SubCount++
		case "video":
			if m.Video != nil {
				continue
			}
			v := &Video{
				Codec:          strings.ToLower(s.CodecName),
				Width:          s.Width,
				Height:         s.Height,
				PixFmt:         s.PixFmt,
				BitRate:        int64(parseFloat(s.BitRate)),
				FPS:            parseRate(s.AvgFrameRate),
				ColorTransfer:  s.ColorTransfer,
				ColorPrimaries: s.ColorPrimaries,
				ColorSpace:     s.ColorSpace,
				BitDepth:       bitDepth(s.PixFmt, s.BitsPerRawSample),
				FieldOrder:     s.FieldOrder,
			}
			if v.FPS == 0 {
				v.FPS = parseRate(s.RFrameRate)
			}
			if v.BitRate == 0 {
				v.BitRate = int64(parseFloat(r.Format.BitRate))
			}
			for _, side := range s.SideData {
				lt := strings.ToLower(side.SideDataType)
				if strings.Contains(lt, "dovi") || strings.Contains(lt, "dolby vision") {
					v.DolbyVision = true
					v.DVProfile = side.DVProfile
					v.DVCompatID = side.DVCompat
				}
				if strings.Contains(lt, "mastering display") || strings.Contains(lt, "content light") {
					v.HDR = true
				}
			}
			if strings.EqualFold(v.ColorTransfer, "smpte2084") {
				v.HDR = true
			}
			if strings.EqualFold(v.ColorTransfer, "arib-std-b67") {
				v.HLG = true
				v.HDR = true
			}
			if d := parseFloat(s.Duration); d > m.Duration {
				m.Duration = d
			}
			m.Video = v
		}
	}
	return m, nil
}

func bitDepth(pix, bits string) int {
	if n, err := strconv.Atoi(bits); err == nil && n > 0 {
		return n
	}
	p := strings.ToLower(pix)
	switch {
	case strings.Contains(p, "p012"), strings.Contains(p, "12le"), strings.Contains(p, "12be"):
		return 12
	case strings.Contains(p, "p010"), strings.Contains(p, "p10"), strings.Contains(p, "10le"), strings.Contains(p, "10be"):
		return 10
	case p == "":
		return 0
	default:
		return 8
	}
}

func parseRate(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		return parseFloat(s)
	}
	nf := parseFloat(n)
	df := parseFloat(d)
	if df == 0 {
		return 0
	}
	return nf / df
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
