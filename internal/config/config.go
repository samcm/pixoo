// Package config loads the daemon configuration from YAML.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen   string           `yaml:"listen"`
	Timezone string           `yaml:"timezone"`
	Device   Device           `yaml:"device"`
	Stream   Stream           `yaml:"stream"`
	Beacon   Beacon           `yaml:"beacon"`
	Scenes   map[string]Scene `yaml:"scenes"`
	Rotation []Rotation       `yaml:"rotation"`
	Log      Log              `yaml:"log"`
}

type Stream struct {
	MaxFrames       int           `yaml:"max_frames"`
	FrameDelay      time.Duration `yaml:"frame_delay"`
	FlushAfter      time.Duration `yaml:"flush_after"`
	MinClipInterval time.Duration `yaml:"min_clip_interval"`
	SourceLease     time.Duration `yaml:"source_lease"`
}

type Device struct {
	Host              string        `yaml:"host"`
	Endpoint          string        `yaml:"endpoint"`
	FrameInterval     time.Duration `yaml:"frame_interval"`
	RequestTimeout    time.Duration `yaml:"request_timeout"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	RefreshEvery      time.Duration `yaml:"refresh_every"`
	GifIDResetEvery   int           `yaml:"gif_id_reset_every"`
	RebootAfterPushes int           `yaml:"reboot_after_pushes"`
	Brightness        *int          `yaml:"brightness"`
	SyncTime          bool          `yaml:"sync_time"`
}

type Beacon struct {
	URL        string   `yaml:"url"`
	Validators []uint64 `yaml:"validators"`
}

type Scene struct {
	Kind    string         `yaml:"kind"`
	Options map[string]any `yaml:"options"`
}

type Rotation struct {
	Scene    string        `yaml:"scene"`
	Duration time.Duration `yaml:"duration"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

func Default() Config {
	return Config{
		Listen:   ":6464",
		Timezone: "Local",
		Device: Device{
			FrameInterval:  time.Second,
			RequestTimeout: 10 * time.Second,
			// The stock ESP-IDF server drops an idle HTTP session after five
			// seconds and has no LRU socket eviction. Keep the one connection
			// active instead of continually creating new sessions.
			HeartbeatInterval: 2 * time.Second,
			RefreshEvery:      30 * time.Minute,
			GifIDResetEvery:   32,
			RebootAfterPushes: 0,
			SyncTime:          true,
		},
		Stream: Stream{
			MaxFrames:       8,
			FrameDelay:      time.Second,
			FlushAfter:      30 * time.Second,
			MinClipInterval: 5 * time.Minute,
			SourceLease:     2 * time.Minute,
		},
		Log: Log{Level: "info", Format: "text"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()

	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}

	if env := os.Getenv("PIXOO_HOST"); env != "" {
		cfg.Device.Host = env
	}

	if cfg.Device.Host == "" {
		return cfg, fmt.Errorf("config: device.host is required")
	}

	if cfg.Scenes == nil {
		cfg.Scenes = map[string]Scene{}
	}

	if _, ok := cfg.Scenes["clock"]; !ok {
		cfg.Scenes["clock"] = Scene{Kind: "clock"}
	}

	if _, ok := cfg.Scenes["beacon"]; !ok && cfg.Beacon.URL != "" {
		cfg.Scenes["beacon"] = Scene{Kind: "beacon"}
	}

	if len(cfg.Rotation) == 0 {
		cfg.Rotation = append(cfg.Rotation, Rotation{Scene: "clock", Duration: 5 * time.Minute})

		if cfg.Beacon.URL != "" {
			cfg.Rotation = append(cfg.Rotation, Rotation{Scene: "beacon", Duration: 5 * time.Minute})
		}
	}

	for i, r := range cfg.Rotation {
		if _, ok := cfg.Scenes[r.Scene]; !ok {
			return cfg, fmt.Errorf("config: rotation[%d] references unknown scene %q", i, r.Scene)
		}

		if r.Duration <= 0 {
			cfg.Rotation[i].Duration = 5 * time.Minute
		}
	}

	if cfg.Device.Brightness != nil && (*cfg.Device.Brightness < 0 || *cfg.Device.Brightness > 100) {
		return cfg, fmt.Errorf("config: device.brightness must be 0-100")
	}
	if cfg.Stream.MaxFrames < 1 || cfg.Stream.MaxFrames > 60 {
		return cfg, fmt.Errorf("config: stream.max_frames must be 1-60")
	}
	if cfg.Stream.FrameDelay < 50*time.Millisecond {
		return cfg, fmt.Errorf("config: stream.frame_delay must be at least 50ms")
	}
	if cfg.Stream.FlushAfter <= 0 {
		return cfg, fmt.Errorf("config: stream.flush_after must be positive")
	}
	if cfg.Stream.MinClipInterval <= 0 {
		return cfg, fmt.Errorf("config: stream.min_clip_interval must be positive")
	}
	if cfg.Stream.SourceLease <= 0 {
		return cfg, fmt.Errorf("config: stream.source_lease must be positive")
	}

	return cfg, nil
}

func (c Config) Location() (*time.Location, error) {
	if c.Timezone == "" || c.Timezone == "Local" {
		return time.Local, nil
	}

	return time.LoadLocation(c.Timezone)
}
