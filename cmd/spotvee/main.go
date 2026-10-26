// Command spotvee runs the Spotvee server: it watches Spotify playback,
// extracts album-art colors and syncs them to selected Govee lights, and
// serves the HTTP/WebSocket API for the mobile app.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sh4wnK/Spotvee/internal/api"
	"github.com/Sh4wnK/Spotvee/internal/art"
	"github.com/Sh4wnK/Spotvee/internal/auth"
	"github.com/Sh4wnK/Spotvee/internal/config"
	"github.com/Sh4wnK/Spotvee/internal/devices"
	"github.com/Sh4wnK/Spotvee/internal/engine"
	"github.com/Sh4wnK/Spotvee/internal/govee"
	"github.com/Sh4wnK/Spotvee/internal/metrics"
	"github.com/Sh4wnK/Spotvee/internal/palette"
	"github.com/Sh4wnK/Spotvee/internal/spotify"
	"github.com/Sh4wnK/Spotvee/internal/store"
	"github.com/Sh4wnK/Spotvee/internal/sync"
	"github.com/Sh4wnK/Spotvee/internal/watcher"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Spotify client + persisted token.
	spotifyClient, err := spotify.NewClient(spotify.Options{
		ClientID:     cfg.SpotifyClientID,
		ClientSecret: cfg.SpotifyClientSecret,
		RedirectURI:  cfg.SpotifyRedirectURI,
	})
	if err != nil {
		return err
	}
	locker := store.NewTokenLocker(cfg.TokenKeyFile, cfg.TokenFile)
	flow, err := auth.New(auth.Options{Client: spotifyClient, Locker: locker, Logger: logger})
	if err != nil {
		return err
	}

	// Light controller: the shared Govee wrapper via the internal adapter.
	ctrl := govee.New(cfg.GoveeAPIKey)

	// Engine + artifact pipeline.
	eng := engine.New(ctrl, logger, engine.DefaultOptions())
	fetcher := art.New(art.Options{
		MaxEntries:    cfg.ImageCacheEntries,
		MaxBytes:      cfg.ImageCacheBytes,
		MaxImageBytes: cfg.MaxArtBytes,
	})
	coord := sync.New(eng, fetcher, logger, sync.Options{
		CycleInterval: cfg.PollInterval / 2,
		Palette: palette.Options{
			MaxDimension: 256,
			PaletteSize:  cfg.PaletteSize,
			SatFloor:     cfg.SatFloor,
			LumHigh:      cfg.LumHigh,
			LumLow:       cfg.LumLow,
			VividWeight:  cfg.VividWeight,
			VividBase:    0.45,
		},
	})
	coord.SetSyncing(false, cfg.PaletteCycle) // start off until the user enables sync

	// Device source (list + metadata refresh).
	src := devices.NewSource(ctrl)

	// Playback watcher.
	watch := watcher.New(spotifyClient, coord.HandleTrack, logger, watcher.Config{
		BaseInterval: cfg.PollInterval,
		MinInterval:  cfg.MinPollInterval,
		MaxInterval:  cfg.MaxPollInterval,
		FastLead:     cfg.FastPollLead,
	})
	watch.Start()
	defer watch.Stop()

	// Apply-latency metrics: the engine's histogram feeds both /api/metrics
	// and /metrics (Prometheus text).
	appMetrics := metrics.NewAppMetrics(eng.LatencyHistogram())
	appMetrics.SetExposed(cfg.MetricsExpose)

	// API server.
	srv := api.New(api.Options{
		Logger:       logger,
		Auth:         flow,
		Coordinator:  coord,
		Engine:       eng,
		DeviceSource: src,
		AppToken:     cfg.AppToken,
		Metrics:      appMetrics,
	})

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("Spotvee listening", "addr", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}
	eng.Close()
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
