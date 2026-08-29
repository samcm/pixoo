package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samcm/pixoo/internal/app"
	"github.com/samcm/pixoo/internal/beacon"
	"github.com/samcm/pixoo/internal/config"
	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/scene"
	"github.com/samcm/pixoo/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to the YAML config")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)

	loc, err := cfg.Location()
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}

	client := pixoo.New(cfg.Device.Host, pixoo.Options{
		Endpoint:          cfg.Device.Endpoint,
		FrameInterval:     cfg.Device.FrameInterval,
		RequestTimeout:    cfg.Device.RequestTimeout,
		RefreshEvery:      cfg.Device.RefreshEvery,
		GifIDResetEvery:   cfg.Device.GifIDResetEvery,
		RebootAfterPushes: cfg.Device.RebootAfterPushes,
	}, logger)

	deps := scene.Deps{
		Weather:    client,
		Validators: cfg.Beacon.Validators,
		Location:   loc,
		Logger:     logger,
	}

	if cfg.Beacon.URL != "" {
		deps.Beacon = beacon.New(cfg.Beacon.URL)
	}

	scenes := make(map[string]scene.Scene, len(cfg.Scenes)+4)

	for name, sc := range cfg.Scenes {
		s, err := scene.New(sc.Kind, name, sc.Options, deps)
		if err != nil {
			return err
		}

		scenes[name] = s
	}

	for _, builtin := range []string{"text", "image", "off"} {
		if _, ok := scenes[builtin]; ok {
			continue
		}

		s, err := scene.New(builtin, builtin, nil, deps)
		if err != nil {
			return err
		}

		scenes[builtin] = s
	}

	// The buffered stream is a transport feature rather than part of the
	// configured rotation, so it is always available under a stable name.
	scenes["stream"] = scene.NewStream("stream", scene.StreamOptions{
		MaxFrames:   cfg.Stream.MaxFrames,
		FrameDelay:  cfg.Stream.FrameDelay,
		FlushAfter:  cfg.Stream.FlushAfter,
		SourceLease: cfg.Stream.SourceLease,
	})

	rotation := make([]app.Entry, 0, len(cfg.Rotation))
	for _, r := range cfg.Rotation {
		rotation = append(rotation, app.Entry{Scene: r.Scene, Duration: r.Duration})
	}

	a, err := app.New(app.Options{
		Client:            client,
		Scenes:            scenes,
		Rotation:          rotation,
		HeartbeatInterval: cfg.Device.HeartbeatInterval,
		Brightness:        cfg.Device.Brightness,
		SyncTime:          cfg.Device.SyncTime,
		Location:          loc,
		Logger:            logger,
	})
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(a, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)

	go func() {
		logger.Info("listening", slog.String("addr", cfg.Listen))

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	go func() {
		errCh <- a.Run(ctx)
	}()

	select {
	case err = <-errCh:
	case <-ctx.Done():
		err = nil
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	_ = httpServer.Shutdown(shutdownCtx)

	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	logger.Info("stopped")

	return nil
}

func newLogger(cfg config.Log) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler)
}
