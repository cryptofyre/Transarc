package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cryptofyre/transarc/internal/arr"
	"github.com/cryptofyre/transarc/internal/buildinfo"
	"github.com/cryptofyre/transarc/internal/config"
	"github.com/cryptofyre/transarc/internal/ffmpeg"
	"github.com/cryptofyre/transarc/internal/gpu"
	"github.com/cryptofyre/transarc/internal/human"
	"github.com/cryptofyre/transarc/internal/logx"
	"github.com/cryptofyre/transarc/internal/play"
	"github.com/cryptofyre/transarc/internal/probe"
	"github.com/cryptofyre/transarc/internal/runtime"
	"github.com/cryptofyre/transarc/internal/service"
	"github.com/cryptofyre/transarc/internal/single"
	"github.com/cryptofyre/transarc/internal/state"
	"github.com/spf13/cobra"
)

// New returns the root command.
func New() *cobra.Command {
	var configPath string
	root := &cobra.Command{
		Use:   "transarc",
		Short: "Daily AV1 archival encoder for Sonarr and Radarr",
		Long: `Transarc re-encodes movies and episodes to AV1 on Intel Arc GPUs.
An encode replaces the original only when it is smaller by the configured margin
and the duration still matches. Arc uses QSV ICQ. NVIDIA NVENC is a separate
test path and does not share that quality scale.`,
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "config file (default /config/config.yaml or ./config.yaml)")

	root.AddCommand(versionCmd())
	root.AddCommand(initCmd(&configPath))
	root.AddCommand(serviceCmd(&configPath))
	root.AddCommand(runCmd(&configPath))
	root.AddCommand(scanCmd(&configPath))
	root.AddCommand(fileCmd(&configPath))
	root.AddCommand(devicesCmd(&configPath))
	root.AddCommand(statusCmd(&configPath))
	root.AddCommand(healthCmd(&configPath))
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Println("transarc", buildinfo.Version)
		},
	}
}

func initCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write an example config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := *configPath
			if path == "" {
				path = "config.yaml"
			}
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
				return err
			}
			return os.WriteFile(path, []byte(config.Example), 0o600)
		},
	}
}

func serviceCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "service",
		Short: "Run the daily scheduler until stopped",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			runner, cleanup, err := open(ctx, *configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			return runner.Serve(ctx)
		},
	}
}

func runCmd(configPath *string) *cobra.Command {
	var dry bool
	var limit int
	var gpu string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run one pass now",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			runner, cleanup, err := open(ctx, *configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = runner.Pass(ctx, service.Options{DryRun: dry, Limit: limit, GPU: gpu})
			return err
		},
	}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "probe and plan without encoding")
	cmd.Flags().IntVar(&limit, "limit", 0, "encode at most this many files, largest first")
	cmd.Flags().StringVar(&gpu, "gpu", "", "use only this device")
	return cmd
}

func scanCmd(configPath *string) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "List files that would be encoded",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			runner, cleanup, err := open(ctx, *configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = runner.Pass(ctx, service.Options{DryRun: true, Limit: limit})
			return err
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "show at most this many candidates")
	return cmd
}

func fileCmd(configPath *string) *cobra.Command {
	var gpu string
	var ignore bool
	cmd := &cobra.Command{
		Use:   "file PATH",
		Short: "Encode one file and keep it only if it saves space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			runner, cleanup, err := open(ctx, *configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			_, err = runner.Pass(ctx, service.Options{File: args[0], GPU: gpu, IgnoreSavings: ignore})
			return err
		},
	}
	cmd.Flags().StringVar(&gpu, "gpu", "", "device to encode on")
	cmd.Flags().BoolVar(&ignore, "ignore-savings", false, "replace when the encode is any amount smaller")
	return cmd
}

func devicesCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "Show FFmpeg and run a one-second encode on each GPU",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			runner, cleanup, err := open(ctx, *configPath)
			if err != nil {
				return err
			}
			defer cleanup()
			runner.Log.Box("GPU", runner.Backend, []string{runner.FFmpeg.Bin, runnerGPUs(runner)})
			var failed int
			for _, device := range runner.GPUs {
				args, err := ffmpeg.DeviceTestArgs(runner.Backend, device)
				if err != nil {
					return err
				}
				test := runner.FFmpeg
				test.StallAfter = 90 * time.Second
				started := time.Now()
				err = test.Run(ctx, args)
				if err != nil {
					failed++
					runner.Log.Box("FAILED", device, []string{err.Error()})
					continue
				}
				runner.Log.Box("GPU OK", device, []string{"one-second AV1 encode in " + human.Duration(time.Since(started))})
			}
			if failed > 0 {
				return fmt.Errorf("%d GPU test encodes failed", failed)
			}
			return nil
		},
	}
}

func statusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show remembered decisions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			st, err := state.Open(cfg.StateDB)
			if err != nil {
				return err
			}
			defer st.Close()
			sum, err := st.Summary()
			if err != nil {
				return err
			}
			fmt.Printf("encoded %d\nkept %d\nskipped %d\nfailed %d\nsaved %s\n", sum.Encoded, sum.Kept, sum.Skipped, sum.Failed, human.Bytes(sum.SavedBytes))
			return nil
		},
	}
}

func healthCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Exit 0 when config, the database, and ffmpeg are usable",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			if _, err := os.Stat(cfg.FFmpeg.Path); err != nil {
				return fmt.Errorf("ffmpeg: %w", err)
			}
			st, err := state.Open(cfg.StateDB)
			if err != nil {
				return err
			}
			return st.Close()
		},
	}
}

func open(ctx context.Context, configPath string) (*service.Runner, func(), error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	// Read Plex and Jellyfin secrets while still root. The token stays in memory.
	notes := play.FillTokens(&cfg)
	log, closer, err := newLogger(cfg)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { closer.Close() }
	if err := runtime.Prepare(cfg); err != nil {
		cleanup()
		return nil, nil, err
	}
	for _, note := range notes {
		log.Warn("playback: %s", note)
	}
	choice, err := gpu.Resolve(ctx, cfg, cfg.FFmpeg.Path)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	store, err := state.Open(cfg.StateDB)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	lock, err := single.Acquire(cfg.StateDB + ".lock")
	if err != nil {
		store.Close()
		cleanup()
		return nil, nil, err
	}
	clients := map[string]*arr.Client{}
	var items []arr.Item
	var sonarrLink service.ArrLink
	if c := arr.New("sonarr", cfg.Sonarr); c != nil {
		clients["sonarr"] = c
		ver, pingErr := c.Ping(ctx)
		var got []arr.Item
		var stats arr.Stats
		var listErr error
		if pingErr != nil {
			log.Warn("sonarr: %v", pingErr)
		} else {
			got, stats, listErr = c.Items(ctx)
			if listErr != nil {
				log.Warn("sonarr library: %v", listErr)
			} else {
				items = append(items, got...)
			}
		}
		sonarrLink = service.ArrLink{On: true, Version: ver, Count: stats.Count, Files: stats.Files, Total: stats.Total, HasFiles: listErr == nil && pingErr == nil, Noun: "series", Unreachable: pingErr != nil, ListDown: pingErr == nil && listErr != nil}
	}
	var radarrLink service.ArrLink
	if c := arr.New("radarr", cfg.Radarr); c != nil {
		clients["radarr"] = c
		ver, pingErr := c.Ping(ctx)
		var got []arr.Item
		var stats arr.Stats
		var listErr error
		if pingErr != nil {
			log.Warn("radarr: %v", pingErr)
		} else {
			got, stats, listErr = c.Items(ctx)
			if listErr != nil {
				log.Warn("radarr library: %v", listErr)
			} else {
				items = append(items, got...)
			}
		}
		radarrLink = service.ArrLink{On: true, Version: ver, Count: stats.Count, Files: stats.Files, Total: stats.Total, HasFiles: listErr == nil && pingErr == nil, Noun: "movies", Unreachable: pingErr != nil, ListDown: pingErr == nil && listErr != nil}
	}
	if choice.Backend == "qsv" && !choice.Jellyfin {
		log.Warn("ffmpeg does not identify as Jellyfin")
	}
	watch, playback := play.Connect(ctx, cfg, func(err error) {
		log.Warn("playback: %v", err)
	})
	for _, st := range playback {
		if st.Problem != "" && !containsNote(notes, st.Problem) {
			log.Warn("playback: %s", st.Problem)
		}
	}
	runner := &service.Runner{
		Cfg:      cfg,
		Log:      log,
		Backend:  choice.Backend,
		GPUs:     choice.Devices,
		Version:  choice.Version,
		Sonarr:   sonarrLink,
		Radarr:   radarrLink,
		Store:    store,
		Items:    items,
		Clients:  clients,
		Hour12:   human.LocaleHour12(),
		Sessions: watch,
		Players:  playLinks(playback),
		Probe:    probe.Prober{Bin: cfg.FFmpeg.ProbePath},
		FFmpeg: ffmpeg.Runner{
			Bin:        cfg.FFmpeg.Path,
			StallAfter: cfg.StallAfter(),
		},
	}
	return runner, func() {
		lock.Release()
		store.Close()
		closer.Close()
	}, nil
}

func playLinks(stats []play.Status) []service.PlayLink {
	if len(stats) == 0 {
		return nil
	}
	out := make([]service.PlayLink, 0, len(stats))
	for _, st := range stats {
		out = append(out, service.PlayLink{
			On:           st.On,
			Live:         st.Live,
			Name:         st.Name,
			Label:        st.Label,
			Version:      st.Version,
			Playing:      st.Playing,
			Known:        st.Known,
			Unreachable:  st.Unreachable,
			SessionsDown: st.SessionsDown,
		})
	}
	return out
}

func containsNote(notes []string, msg string) bool {
	for _, note := range notes {
		if note == msg {
			return true
		}
	}
	return false
}

func loadConfig(path string) (config.Config, error) {
	found, err := findConfig(path)
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(found)
}

func findConfig(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	if v := os.Getenv("TRANSARC_CONFIG"); v != "" {
		return v, nil
	}
	for _, p := range []string{"/config/config.yaml", "config.yaml"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("config file not found (pass --config, or run transarc init)")
}

func newLogger(cfg config.Config) (*logx.Logger, io.Closer, error) {
	if cfg.LogFile == "" {
		return logx.New(os.Stdout), nopCloser{}, nil
	}
	if err := rotate(cfg.LogFile, 20<<20); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return logx.New(io.MultiWriter(os.Stdout, f)), f, nil
}

func rotate(path string, max int64) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < max {
		return nil
	}
	_ = os.Remove(path + ".1")
	return os.Rename(path, path+".1")
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func runnerGPUs(r *service.Runner) string {
	return fmt.Sprintf("%d device(s): %v", len(r.GPUs), r.GPUs)
}
