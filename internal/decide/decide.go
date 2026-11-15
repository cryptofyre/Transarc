package decide

import (
	"fmt"
	"math"
	"strings"

	"github.com/cryptofyre/transarc/internal/probe"
)

// Action is the outcome of a check.
type Action string

const (
	// Proceed means the file should be encoded, or the encode should be kept.
	Proceed Action = "proceed"
	// Keep means the original file stays and the attempt is remembered.
	Keep Action = "keep"
	// Skip means the file is not a candidate. The reason says whether it is permanent.
	Skip Action = "skip"
)

// Result is a human-readable decision.
type Result struct {
	Action       Action
	Reason       string
	SavingsPct   float64
	SavingsBytes int64
	// Permanent skips are stored so the next daily pass does not re-probe them
	// until the file changes. Transient skips (file still settling) are not stored.
	Permanent bool
}

// PreInput is known before any encode.
type PreInput struct {
	Media                probe.Media
	MinSize              int64
	SkipCodecs           []string
	SkipDolbyVisionProf5 bool
}

// PostInput is known after a full encode.
type PostInput struct {
	Source            probe.Media
	Output            probe.Media
	MinSavingsPct     float64
	DurationTolerance float64
}

// Pre reports files that should never be encoded.
func Pre(in PreInput) Result {
	if in.Media.Video == nil {
		return Result{Action: Skip, Reason: "no video stream", Permanent: true}
	}
	if in.Media.Size < in.MinSize {
		return Result{Action: Skip, Reason: "below minimum size", Permanent: true}
	}
	codec := strings.ToLower(in.Media.Video.Codec)
	for _, skip := range in.SkipCodecs {
		if codec == strings.ToLower(strings.TrimSpace(skip)) {
			return Result{Action: Skip, Reason: "already " + codec, Permanent: true}
		}
	}
	v := in.Media.Video
	if in.SkipDolbyVisionProf5 && v.DolbyVision && dolbyVisionProfile5(*v) {
		return Result{
			Action:    Skip,
			Reason:    "Dolby Vision profile 5 has no HDR10 base layer",
			Permanent: true,
		}
	}
	return Result{Action: Proceed, Reason: "candidate"}
}

func dolbyVisionProfile5(v probe.Video) bool {
	switch v.DVProfile {
	case 5:
		return true
	case 7, 8:
		// These profiles carry an HDR10 or HLG base layer.
		return false
	}
	// ffprobe sometimes omits the profile number. A Dolby Vision stream with
	// no HDR10 or HLG signaling is profile 5 (IPT, no base layer).
	return v.DolbyVision && !v.HDR && !v.HLG
}

// Post compares a finished encode with the source.
// The encode is kept only when duration matches, HDR was not crushed to 8-bit,
// and the file is smaller by at least the configured percentage.
func Post(in PostInput) Result {
	if in.Output.Video == nil {
		return Result{Action: Keep, Reason: "encode has no video stream", Permanent: false}
	}
	if in.Source.Duration > 0 && in.Output.Duration > 0 {
		allowed := math.Max(2, in.Source.Duration*in.DurationTolerance)
		delta := math.Abs(in.Source.Duration - in.Output.Duration)
		if delta > allowed {
			return Result{
				Action: Keep,
				Reason: fmt.Sprintf("duration changed by %.1fs", delta),
			}
		}
	}
	if in.Source.Video != nil && in.Source.Video.HDR && in.Output.Video.BitDepth > 0 && in.Output.Video.BitDepth < 10 {
		return Result{Action: Keep, Reason: "HDR source produced an 8-bit encode"}
	}
	if in.Source.Size <= 0 {
		return Result{Action: Keep, Reason: "source size is unknown"}
	}
	saved := in.Source.Size - in.Output.Size
	pct := float64(saved) / float64(in.Source.Size) * 100
	res := Result{SavingsPct: pct, SavingsBytes: saved}
	if saved <= 0 || pct < in.MinSavingsPct {
		res.Action = Keep
		if saved <= 0 {
			res.Reason = fmt.Sprintf("encode is not smaller (%.1f%%)", pct)
		} else {
			res.Reason = fmt.Sprintf("savings %.1f%% below %.1f%%", pct, in.MinSavingsPct)
		}
		res.Permanent = true
		return res
	}
	res.Action = Proceed
	res.Reason = fmt.Sprintf("savings %.1f%%", pct)
	return res
}

// SampleWorthFull reports whether a short sample is promising enough to spend
// a full encode. Estimates under half the threshold are treated as a loss.
// Negative estimates mean the sample was larger than the source, pro-rated.
func SampleWorthFull(estimatedSavingsPct, minSavingsPct float64) bool {
	if estimatedSavingsPct < 0 {
		return false
	}
	return estimatedSavingsPct >= minSavingsPct*0.5
}

// EstimateSavings pro-rates a sample to the full duration.
func EstimateSavings(sourceBytes, sampleBytes int64, sourceDuration, sampleDuration float64) (pct float64, estBytes int64) {
	if sourceBytes <= 0 || sampleDuration <= 0 || sourceDuration <= 0 {
		return 0, 0
	}
	est := float64(sampleBytes) * (sourceDuration / sampleDuration)
	estBytes = int64(est)
	pct = (1 - est/float64(sourceBytes)) * 100
	return pct, estBytes
}
