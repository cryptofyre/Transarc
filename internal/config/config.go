package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// Config is the on-disk service configuration.
type Config struct {
	Schedule   string `yaml:"schedule"`
	Timezone   string `yaml:"timezone"`
	DryRun     bool   `yaml:"dry_run"`
	RunOnStart bool   `yaml:"run_on_start"`
	TempDir    string `yaml:"temp_dir"`
	LogFile    string `yaml:"log_file"`
	StateDB    string `yaml:"state_db"`
	PUID       int    `yaml:"puid"`
	PGID       int    `yaml:"pgid"`

	FFmpeg  FFmpeg  `yaml:"ffmpeg"`
	Encoder Encoder `yaml:"encoder"`
	Rules   Rules   `yaml:"rules"`
	QSV     QSV     `yaml:"qsv"`
	NVENC   NVENC   `yaml:"nvenc"`
	SVT     SVT     `yaml:"svt"`

	Libraries []Library `yaml:"libraries"`
	Sonarr    Arr       `yaml:"sonarr"`
	Radarr    Arr       `yaml:"radarr"`
	Plex      Play      `yaml:"plex"`
	Jellyfin  Play      `yaml:"jellyfin"`
	API       API       `yaml:"api"`
}

// API is the optional read-only HTTP status API. An empty Listen leaves it off.
// Listen is host:port. ":8790" listens on every interface, which under host
// networking includes every network the server is on.
type API struct {
	Listen string `yaml:"listen"`
	Token  string `yaml:"token"`
}

// FFmpeg locates the Jellyfin build used for Arc encodes.
type FFmpeg struct {
	Path          string `yaml:"path"`
	ProbePath     string `yaml:"probe_path"`
	AllowUpstream bool   `yaml:"allow_upstream"`
}

// Encoder selects the backend. QSV, NVENC, and SVT do not share a quality scale.
type Encoder struct {
	// Backend is auto, qsv, nvenc, or svt.
	Backend string   `yaml:"backend"`
	GPUs    []string `yaml:"gpus"`
}

// Rules decide whether an encode is allowed to replace the original.
type Rules struct {
	MinSavingsPercent float64  `yaml:"min_savings_percent"`
	MinFileSizeMB     int64    `yaml:"min_file_size_mb"`
	SkipCodecs        []string `yaml:"skip_codecs"`
	Extensions        []string `yaml:"extensions"`
	SampleSeconds     int      `yaml:"sample_seconds"`
	SampleStartRatio  float64  `yaml:"sample_start_ratio"`
	RetryDays         int      `yaml:"retry_days"`
	SettleMinutes     int      `yaml:"settle_minutes"`
	DurationTolerance float64  `yaml:"duration_tolerance"`
	StallMinutes      int      `yaml:"stall_minutes"`
	MaxFilesPerRun    int      `yaml:"max_files_per_run"`
	ProbeConcurrency  int      `yaml:"probe_concurrency"`
	SkipDolbyVisionP5 bool     `yaml:"skip_dolby_vision_profile5"`
	ExcludeDirNames   []string `yaml:"exclude_dir_names"`
}

// QSV is the Intel Arc AV1 path (av1_qsv, ICQ via global_quality).
// Lower global_quality is higher quality and a larger file.
// This number is not comparable to nvenc.cq.
type QSV struct {
	Preset         string `yaml:"preset"`
	GlobalQuality  int    `yaml:"global_quality"`
	LookAheadDepth int    `yaml:"look_ahead_depth"`
	GOPSeconds     int    `yaml:"gop_seconds"`
	BFrames        int    `yaml:"b_frames"`
	Refs           int    `yaml:"refs"`
	LowPower       bool   `yaml:"low_power"`
	ExtBRC         bool   `yaml:"extbrc"`
	AdaptiveI      bool   `yaml:"adaptive_i"`
	AdaptiveB      bool   `yaml:"adaptive_b"`
	BStrategy      bool   `yaml:"b_strategy"`
	BitDepth       string `yaml:"bit_depth"` // auto, 8, 10
}

// NVENC is the NVIDIA AV1 path used for parameter tests on non-Arc machines.
// cq uses NVIDIA's constant-quality scale. A cq of 28 is not an ICQ of 28.
type NVENC struct {
	Preset         string `yaml:"preset"`
	Tune           string `yaml:"tune"`
	CQ             int    `yaml:"cq"`
	LookAhead      int    `yaml:"lookahead"`
	BFrames        int    `yaml:"b_frames"`
	AQStrength     int    `yaml:"aq_strength"`
	SpatialAQ      bool   `yaml:"spatial_aq"`
	TemporalAQ     bool   `yaml:"temporal_aq"`
	Multipass      string `yaml:"multipass"`
	BRefMode       string `yaml:"b_ref_mode"`
	GOPSeconds     int    `yaml:"gop_seconds"`
	BitDepth       string `yaml:"bit_depth"`
	LookaheadLevel int    `yaml:"lookahead_level"`
	TemporalFilter int    `yaml:"tf_level"`
}

// SVT is the software AV1 path (libsvtav1) for machines with no AV1 GPU.
// Raspberry Pi 4 and 5 are in this group: neither has an AV1 hardware encoder.
// CRF is SVT's own 1-63 scale. It is not Arc ICQ and it is not NVENC CQ.
// Preset 1 is slowest and 13 is fastest. An omitted preset becomes 8.
type SVT struct {
	Preset     int    `yaml:"preset"`
	CRF        int    `yaml:"crf"`
	GOPSeconds int    `yaml:"gop_seconds"`
	BitDepth   string `yaml:"bit_depth"`
	// Jobs is how many files encode at once. SVT already uses every core,
	// so the default is one job.
	Jobs int `yaml:"jobs"`
}

// Library is one Sonarr or Radarr root as seen inside this container.
type Library struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	Arr  string `yaml:"arr"` // sonarr, radarr, or empty
}

// Arr is a Sonarr or Radarr connection.
type Arr struct {
	URL     string    `yaml:"url"`
	APIKey  string    `yaml:"api_key"`
	PathMap []PathMap `yaml:"path_map"`
}

// PathMap converts a path returned by Sonarr, Radarr, Plex, or Jellyfin
// into the path inside this container.
type PathMap struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Play is a Plex or Jellyfin connection.
// A file that server has open waits. The encode continues once the session ends.
// Token and APIKey are optional when Preferences points at the server's own config.
// TokenErr is set while reading that config and is not stored in YAML.
type Play struct {
	URL         string    `yaml:"url"`
	Token       string    `yaml:"token"`
	APIKey      string    `yaml:"api_key"`
	Preferences string    `yaml:"preferences"`
	PathMap     []PathMap `yaml:"path_map"`
	TokenErr    string    `yaml:"-"`
}

// Secret is the token or API key already in memory.
func (p Play) Secret() string {
	if s := strings.TrimSpace(p.Token); s != "" {
		return s
	}
	return strings.TrimSpace(p.APIKey)
}

// Active reports whether this server should be consulted.
// An empty example block is not active.
func (p Play) Active() bool {
	return strings.TrimSpace(p.URL) != "" || strings.TrimSpace(p.Preferences) != "" || p.Secret() != "" || p.TokenErr != ""
}

// Defaults fills zero values. It does not invent library paths or API keys.
func (c *Config) Defaults() {
	if c.Schedule == "" {
		c.Schedule = "0 3 * * *"
	}
	if c.Timezone == "" {
		c.Timezone = "America/Chicago"
	}
	if c.TempDir == "" {
		c.TempDir = "/temp"
	}
	if c.StateDB == "" {
		c.StateDB = "/config/transarc.db"
	}
	if c.Encoder.Backend == "" {
		c.Encoder.Backend = "auto"
	}
	if c.FFmpeg.Path == "" {
		c.FFmpeg.Path = envOr("TRANSARC_FFMPEG", "/usr/lib/jellyfin-ffmpeg/ffmpeg")
	}
	if c.FFmpeg.ProbePath == "" {
		c.FFmpeg.ProbePath = envOr("TRANSARC_FFPROBE", "/usr/lib/jellyfin-ffmpeg/ffprobe")
	}
	if c.Rules.MinSavingsPercent == 0 {
		c.Rules.MinSavingsPercent = 10
	}
	if c.Rules.MinFileSizeMB == 0 {
		c.Rules.MinFileSizeMB = 200
	}
	if len(c.Rules.SkipCodecs) == 0 {
		c.Rules.SkipCodecs = []string{"av1"}
	}
	if len(c.Rules.Extensions) == 0 {
		c.Rules.Extensions = []string{"mkv", "mp4", "m4v", "avi", "ts", "m2ts"}
	}
	if c.Rules.SampleSeconds == 0 {
		c.Rules.SampleSeconds = 30
	}
	if c.Rules.SampleStartRatio == 0 {
		c.Rules.SampleStartRatio = 0.2
	}
	if c.Rules.RetryDays == 0 {
		c.Rules.RetryDays = 21
	}
	if c.Rules.DurationTolerance == 0 {
		c.Rules.DurationTolerance = 0.01
	}
	if c.Rules.StallMinutes == 0 {
		c.Rules.StallMinutes = 20
	}
	if c.Rules.ProbeConcurrency == 0 {
		c.Rules.ProbeConcurrency = 4
	}
	if c.Rules.ExcludeDirNames == nil {
		c.Rules.ExcludeDirNames = []string{
			"featurettes", "extras", "interviews", "scenes", "shorts",
			"trailers", "samples", "deleted scenes", "behind the scenes", "other",
		}
	}
	// Profile 5 has no HDR10 base layer. Skipping it is the safe default.
	// The zero value of a bool cannot tell "unset" from "false", so the
	// example config sets this explicitly and Load treats a missing file
	// via Defaults only when the field was not in YAML. See applyBoolDefault.
	if c.QSV.Preset == "" {
		c.QSV.Preset = "veryslow"
	}
	if c.QSV.GlobalQuality == 0 {
		c.QSV.GlobalQuality = 23
	}
	if c.QSV.LookAheadDepth == 0 {
		c.QSV.LookAheadDepth = 40
	}
	if c.QSV.GOPSeconds == 0 {
		c.QSV.GOPSeconds = 10
	}
	if c.QSV.BFrames == 0 {
		c.QSV.BFrames = 7
	}
	if c.QSV.Refs == 0 {
		c.QSV.Refs = 5
	}
	if c.QSV.BitDepth == "" {
		c.QSV.BitDepth = "auto"
	}
	if c.NVENC.Preset == "" {
		c.NVENC.Preset = "p7"
	}
	if c.NVENC.Tune == "" {
		// uhq exists on current NVENC (FFmpeg 7+ / Blackwell). It is not an Arc option.
		c.NVENC.Tune = "uhq"
	}
	if c.NVENC.CQ == 0 {
		c.NVENC.CQ = 28
	}
	if c.NVENC.LookAhead == 0 {
		c.NVENC.LookAhead = 32
	}
	if c.NVENC.BFrames == 0 {
		c.NVENC.BFrames = 4
	}
	if c.NVENC.AQStrength == 0 {
		c.NVENC.AQStrength = 8
	}
	if c.NVENC.Multipass == "" {
		c.NVENC.Multipass = "fullres"
	}
	if c.NVENC.BRefMode == "" {
		c.NVENC.BRefMode = "middle"
	}
	if c.NVENC.GOPSeconds == 0 {
		c.NVENC.GOPSeconds = 10
	}
	if c.NVENC.BitDepth == "" {
		c.NVENC.BitDepth = "auto"
	}
	if c.NVENC.LookaheadLevel == 0 {
		c.NVENC.LookaheadLevel = 3
	}
	if c.NVENC.TemporalFilter == 0 {
		c.NVENC.TemporalFilter = 4
	}
	// Preset 0 and CRF 0 are the YAML zero values. SVT preset 0 is legal but
	// slow enough to stall a Pi for days, so an omitted preset becomes 8.
	if c.SVT.Preset == 0 {
		c.SVT.Preset = 8
	}
	if c.SVT.CRF == 0 {
		c.SVT.CRF = 32
	}
	if c.SVT.GOPSeconds == 0 {
		c.SVT.GOPSeconds = 10
	}
	if c.SVT.BitDepth == "" {
		c.SVT.BitDepth = "auto"
	}
	if c.SVT.Jobs == 0 {
		c.SVT.Jobs = 1
	}
}

// presetBools records which quality toggles were present in YAML.
// LowPower, ExtBRC, and the AQ flags default to on when omitted.
type presetBools struct {
	QSVLowPower     bool
	QSVExtBRC       bool
	QSVAdaptiveI    bool
	QSVAdaptiveB    bool
	QSVBStrategy    bool
	NVENCSpatialAQ  bool
	NVENCTemporalAQ bool
	SkipDVP5        bool
	Settle          bool
}

// Load reads path, applies environment overrides, and checks the schedule.
func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	present, err := yamlBools(b)
	if err != nil {
		return Config{}, err
	}
	c.Defaults()
	if !present.QSVLowPower {
		c.QSV.LowPower = true
	}
	if !present.QSVExtBRC {
		c.QSV.ExtBRC = true
	}
	if !present.QSVAdaptiveI {
		c.QSV.AdaptiveI = true
	}
	if !present.QSVAdaptiveB {
		c.QSV.AdaptiveB = true
	}
	if !present.QSVBStrategy {
		c.QSV.BStrategy = true
	}
	if !present.NVENCSpatialAQ {
		c.NVENC.SpatialAQ = true
	}
	if !present.NVENCTemporalAQ {
		c.NVENC.TemporalAQ = true
	}
	if !present.SkipDVP5 {
		c.Rules.SkipDolbyVisionP5 = true
	}
	if !present.Settle {
		c.Rules.SettleMinutes = 15
	}
	c.applyEnv()
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func yamlBools(b []byte) (presetBools, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return presetBools{}, err
	}
	var p presetBools
	if q, ok := raw["qsv"].(map[string]any); ok {
		_, p.QSVLowPower = q["low_power"]
		_, p.QSVExtBRC = q["extbrc"]
		_, p.QSVAdaptiveI = q["adaptive_i"]
		_, p.QSVAdaptiveB = q["adaptive_b"]
		_, p.QSVBStrategy = q["b_strategy"]
	}
	if n, ok := raw["nvenc"].(map[string]any); ok {
		_, p.NVENCSpatialAQ = n["spatial_aq"]
		_, p.NVENCTemporalAQ = n["temporal_aq"]
	}
	if r, ok := raw["rules"].(map[string]any); ok {
		_, p.SkipDVP5 = r["skip_dolby_vision_profile5"]
		_, p.Settle = r["settle_minutes"]
	}
	return p, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("PUID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PUID = n
		}
	}
	if v := os.Getenv("PGID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PGID = n
		}
	}
	if v := os.Getenv("TRANSARC_DRY_RUN"); v == "1" || strings.EqualFold(v, "true") {
		c.DryRun = true
	}
	if v := os.Getenv("TZ"); v != "" && os.Getenv("TRANSARC_RESPECT_TZ") == "1" {
		c.Timezone = v
	}
	if v := os.Getenv("TRANSARC_SONARR_URL"); v != "" {
		c.Sonarr.URL = v
	}
	if v := os.Getenv("TRANSARC_SONARR_API_KEY"); v != "" {
		c.Sonarr.APIKey = v
	}
	if v := os.Getenv("TRANSARC_RADARR_URL"); v != "" {
		c.Radarr.URL = v
	}
	if v := os.Getenv("TRANSARC_RADARR_API_KEY"); v != "" {
		c.Radarr.APIKey = v
	}
	if v := os.Getenv("TRANSARC_FFMPEG"); v != "" {
		c.FFmpeg.Path = v
	}
	if v := os.Getenv("TRANSARC_FFPROBE"); v != "" {
		c.FFmpeg.ProbePath = v
	}
	if v := os.Getenv("TRANSARC_API_LISTEN"); v != "" {
		c.API.Listen = v
	}
	if v := os.Getenv("TRANSARC_API_TOKEN"); v != "" {
		c.API.Token = v
	}
	applyPlayEnv("TRANSARC_PLEX", &c.Plex)
	applyPlayEnv("TRANSARC_JELLYFIN", &c.Jellyfin)
}

func applyPlayEnv(prefix string, p *Play) {
	if v := os.Getenv(prefix + "_URL"); v != "" {
		p.URL = v
	}
	if v := os.Getenv(prefix + "_TOKEN"); v != "" {
		p.Token = v
	}
	if v := os.Getenv(prefix + "_API_KEY"); v != "" {
		p.APIKey = v
	}
	if v := os.Getenv(prefix + "_CONFIG"); v != "" {
		p.Preferences = v
	}
	if v := os.Getenv(prefix + "_PATH_MAP"); v != "" {
		p.PathMap = ParsePathMap(v)
	}
}

// Validate checks values that would otherwise fail deep in a pass.
func (c Config) Validate() error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(c.Schedule); err != nil {
		return fmt.Errorf("schedule %q: %w", c.Schedule, err)
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone %q: %w", c.Timezone, err)
	}
	switch c.Encoder.Backend {
	case "auto", "qsv", "nvenc", "svt":
	default:
		return fmt.Errorf("encoder.backend must be auto, qsv, nvenc, or svt")
	}
	if c.Rules.MinSavingsPercent < 0 || c.Rules.MinSavingsPercent > 100 {
		return fmt.Errorf("min_savings_percent must be between 0 and 100")
	}
	if c.QSV.GlobalQuality < 1 || c.QSV.GlobalQuality > 51 {
		return fmt.Errorf("qsv.global_quality must be between 1 and 51")
	}
	if c.NVENC.CQ < 0 || c.NVENC.CQ > 51 {
		return fmt.Errorf("nvenc.cq must be between 0 and 51")
	}
	if c.SVT.CRF < 1 || c.SVT.CRF > 63 {
		return fmt.Errorf("svt.crf must be between 1 and 63")
	}
	if c.SVT.Preset < 1 || c.SVT.Preset > 13 {
		return fmt.Errorf("svt.preset must be between 1 and 13")
	}
	if c.SVT.Jobs < 1 || c.SVT.Jobs > 8 {
		return fmt.Errorf("svt.jobs must be between 1 and 8")
	}
	if l := strings.TrimSpace(c.API.Listen); l != "" {
		if _, port, err := net.SplitHostPort(l); err != nil || port == "" {
			return fmt.Errorf("api.listen %q must be host:port, for example :8790 or 10.0.0.5:8790", c.API.Listen)
		}
	}
	if c.Rules.SampleStartRatio < 0 || c.Rules.SampleStartRatio >= 1 {
		return fmt.Errorf("sample_start_ratio must be in [0, 1)")
	}
	return nil
}

// Location returns the configured time zone.
func (c Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.Local
	}
	return loc
}

// RetryAfter is how long a "kept original" decision suppresses another attempt.
func (c Config) RetryAfter() time.Duration {
	return time.Duration(c.Rules.RetryDays) * 24 * time.Hour
}

// StallAfter is how long an encode may sit on the same frame.
func (c Config) StallAfter() time.Duration {
	return time.Duration(c.Rules.StallMinutes) * time.Minute
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
