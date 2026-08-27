// Package app owns the display loop: it picks the scene to show, renders it,
// pushes the frame to the device and keeps the device link healthy.
package app

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/render"
	"github.com/samcm/pixoo/internal/scene"
)

type Entry struct {
	Scene    string        `json:"scene"`
	Duration time.Duration `json:"duration"`
}

type Options struct {
	Client            *pixoo.Client
	Scenes            map[string]scene.Scene
	Rotation          []Entry
	HeartbeatInterval time.Duration
	Brightness        *int
	SyncTime          bool
	Location          *time.Location
	Logger            *slog.Logger
}

type override struct {
	scene string
	until time.Time
}

type App struct {
	client     *pixoo.Client
	scenes     map[string]scene.Scene
	rotation   []Entry
	heartbeat  time.Duration
	brightness *int
	syncTime   bool
	loc        *time.Location
	logger     *slog.Logger
	started    time.Time
	wake       chan struct{}

	mu          sync.Mutex
	current     string
	rotIdx      int
	rotStart    time.Time
	override    *override
	preview     *image.RGBA
	screenOff   bool
	brightNow   int
	forceReboot bool
}

func New(o Options) (*App, error) {
	if o.Client == nil {
		return nil, errors.New("app: client is required")
	}

	if len(o.Rotation) == 0 {
		return nil, errors.New("app: rotation is empty")
	}

	for _, e := range o.Rotation {
		if _, ok := o.Scenes[e.Scene]; !ok {
			return nil, fmt.Errorf("app: rotation references unknown scene %q", e.Scene)
		}
	}

	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = time.Minute
	}

	if o.Location == nil {
		o.Location = time.Local
	}

	if o.Logger == nil {
		o.Logger = slog.Default()
	}

	return &App{
		client:     o.Client,
		scenes:     o.Scenes,
		rotation:   o.Rotation,
		heartbeat:  o.HeartbeatInterval,
		brightness: o.Brightness,
		syncTime:   o.SyncTime,
		loc:        o.Location,
		logger:     o.Logger.WithGroup("app"),
		wake:       make(chan struct{}, 1),
		brightNow:  -1,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	a.started = time.Now()
	a.setup(ctx)

	go a.heartbeatLoop(ctx)

	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		a.mu.Lock()
		forced := a.forceReboot
		a.forceReboot = false
		a.mu.Unlock()

		if forced || a.client.NeedsReboot() {
			a.rebootPanel(ctx)

			continue
		}

		now := time.Now()
		sc := a.pick(now)

		frame, next, err := sc.Render(ctx, now)
		if err != nil {
			a.logger.Warn("render failed", slog.String("scene", sc.Name()), slog.String("error", err.Error()))
			next = 5 * time.Second
		} else if err := a.display(ctx, frame); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			a.logger.Debug("push failed", slog.String("scene", sc.Name()), slog.String("error", err.Error()), slog.Duration("backoff", backoff))
			a.sleep(ctx, backoff)

			backoff = min(backoff*2, 30*time.Second)

			continue
		} else {
			backoff = time.Second
		}

		if next <= 0 {
			next = time.Second
		}

		a.sleep(ctx, a.clamp(next, time.Now()))
	}
}

func (a *App) setup(ctx context.Context) {
	if conf, err := a.client.GetConf(ctx); err == nil {
		a.logger.Info("device reached",
			slog.String("host", a.client.Host()),
			slog.Int("brightness", conf.Brightness),
			slog.Int("channel", conf.ChannelIndex),
			slog.Int("clock_id", conf.ClockID),
			slog.Bool("screen_on", conf.LightSwitch))

		a.mu.Lock()
		a.brightNow = conf.Brightness
		a.mu.Unlock()
	} else {
		a.logger.Warn("device not reachable yet", slog.String("host", a.client.Host()), slog.String("error", err.Error()))
	}

	if a.syncTime {
		if err := a.client.SyncTime(ctx, a.loc); err != nil {
			a.logger.Warn("time sync failed", slog.String("error", err.Error()))
		}
	}

	if a.brightness != nil {
		if err := a.SetBrightness(ctx, *a.brightness); err != nil {
			a.logger.Warn("brightness set failed", slog.String("error", err.Error()))
		}
	}
}

// rebootPanel restarts the panel to reclaim the heap its firmware leaks on
// every pushed frame, then waits for it to answer again.
func (a *App) rebootPanel(ctx context.Context) {
	a.client.Reboot(ctx)
	a.sleep(ctx, 20*time.Second)

	for attempt := 0; attempt < 12 && ctx.Err() == nil; attempt++ {
		if err := a.client.Heartbeat(ctx); err == nil {
			a.logger.Info("panel back after reboot")
			a.client.Invalidate()

			return
		}

		a.sleep(ctx, 5*time.Second)
	}

	a.logger.Warn("panel did not come back after reboot")
}

// RebootPanel is the operator-triggered version of the push-budget reboot.
func (a *App) RebootPanel() {
	a.mu.Lock()
	a.forceReboot = true
	a.mu.Unlock()

	a.nudge()
}

func (a *App) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.heartbeat / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		st := a.client.Status()
		if st.Online && time.Since(st.LastOK) < a.heartbeat {
			continue
		}

		if err := a.client.Heartbeat(ctx); err != nil {
			continue
		}

		if !st.Online {
			a.client.Invalidate()
			a.nudge()
		}
	}
}

func (a *App) display(ctx context.Context, frame scene.Frame) error {
	a.mu.Lock()
	wasOff := a.screenOff
	a.mu.Unlock()

	if frame.ScreenOff {
		if !wasOff {
			if err := a.client.SetScreen(ctx, false); err != nil {
				return err
			}

			a.mu.Lock()
			a.screenOff = true
			a.preview = nil
			a.mu.Unlock()
		}

		return nil
	}

	if wasOff {
		if err := a.client.SetScreen(ctx, true); err != nil {
			return err
		}

		a.mu.Lock()
		a.screenOff = false
		a.mu.Unlock()

		a.client.Invalidate()
	}

	var err error

	if len(frame.Frames) > 0 {
		rgbs := make([][]byte, 0, len(frame.Frames))
		for _, f := range frame.Frames {
			rgbs = append(rgbs, render.RGB(f))
		}

		_, err = a.client.PushAnimation(ctx, rgbs, frame.Delay)
	} else {
		_, err = a.client.PushFrame(ctx, render.RGB(frame.Image))
	}

	if err != nil {
		return err
	}

	a.mu.Lock()
	a.preview = frame.Preview()
	a.mu.Unlock()

	return nil
}

func (a *App) pick(now time.Time) scene.Scene {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.override != nil && !a.override.until.IsZero() && now.After(a.override.until) {
		a.override = nil
		a.rotStart = now
	}

	name := ""

	if a.override != nil {
		name = a.override.scene
	} else {
		if a.rotStart.IsZero() {
			a.rotStart = now
		}

		if now.Sub(a.rotStart) >= a.rotation[a.rotIdx].Duration {
			a.rotIdx = (a.rotIdx + 1) % len(a.rotation)
			a.rotStart = now
		}

		name = a.rotation[a.rotIdx].Scene
	}

	if name != a.current {
		a.logger.Info("scene", slog.String("name", name), slog.Bool("override", a.override != nil))
		a.current = name
	}

	return a.scenes[name]
}

// clamp shortens a scene's requested sleep so scene switches happen on time.
func (a *App) clamp(next time.Duration, now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()

	deadline := now.Add(next)

	switch {
	case a.override != nil && !a.override.until.IsZero() && a.override.until.Before(deadline):
		deadline = a.override.until
	case a.override == nil:
		if end := a.rotStart.Add(a.rotation[a.rotIdx].Duration); end.Before(deadline) {
			deadline = end
		}
	}

	return max(time.Until(deadline), 200*time.Millisecond)
}

func (a *App) sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-a.wake:
	case <-timer.C:
	}
}

func (a *App) nudge() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Show puts a scene on screen for d, or until Resume when d is zero.
func (a *App) Show(name string, d time.Duration) error {
	if _, ok := a.scenes[name]; !ok {
		return fmt.Errorf("unknown scene %q", name)
	}

	a.mu.Lock()
	o := &override{scene: name}
	if d > 0 {
		o.until = time.Now().Add(d)
	}

	a.override = o
	a.mu.Unlock()

	a.nudge()

	return nil
}

func (a *App) Resume() {
	a.mu.Lock()
	a.override = nil
	a.rotStart = time.Now()
	a.mu.Unlock()

	a.nudge()
}

func (a *App) SetBrightness(ctx context.Context, v int) error {
	if err := a.client.SetBrightness(ctx, v); err != nil {
		return err
	}

	a.mu.Lock()
	a.brightNow = v
	a.mu.Unlock()

	return nil
}

func (a *App) SetScreen(on bool) error {
	if on {
		a.Resume()

		return nil
	}

	return a.Show("off", 0)
}

func (a *App) SetText(opts scene.TextOptions, d time.Duration) error {
	t, ok := a.scenes["text"].(*scene.Text)
	if !ok {
		return errors.New("text scene not available")
	}

	t.Set(opts)
	a.client.Invalidate()

	return a.Show("text", d)
}

func (a *App) SetImage(data []byte, label string, d time.Duration) error {
	img, ok := a.scenes["image"].(*scene.Image)
	if !ok {
		return errors.New("image scene not available")
	}

	if err := img.Set(data, label); err != nil {
		return err
	}

	a.client.Invalidate()

	return a.Show("image", d)
}

func (a *App) Command(ctx context.Context, command string, args map[string]any) (map[string]any, error) {
	return a.client.Command(ctx, command, args)
}

// Preview returns a copy of the last frame sent, or a black frame.
func (a *App) Preview() *image.RGBA {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.preview == nil {
		return render.New().Img
	}

	return render.Copy(a.preview)
}

type SceneInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (a *App) Scenes() []SceneInfo {
	out := make([]SceneInfo, 0, len(a.scenes))
	for name, s := range a.scenes {
		out = append(out, SceneInfo{Name: name, Kind: s.Kind()})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

type Override struct {
	Scene string     `json:"scene"`
	Until *time.Time `json:"until,omitempty"`
}

type Status struct {
	Host          string       `json:"host"`
	Device        pixoo.Status `json:"device"`
	Scene         string       `json:"scene"`
	Kind          string       `json:"kind"`
	Override      *Override    `json:"override,omitempty"`
	Rotation      []Entry      `json:"rotation"`
	RotationIndex int          `json:"rotation_index"`
	RotationEnds  time.Time    `json:"rotation_ends"`
	ScreenOff     bool         `json:"screen_off"`
	Brightness    int          `json:"brightness"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	Now           time.Time    `json:"now"`
}

func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()

	st := Status{
		Host:          a.client.Host(),
		Device:        a.client.Status(),
		Scene:         a.current,
		Rotation:      a.rotation,
		RotationIndex: a.rotIdx,
		RotationEnds:  a.rotStart.Add(a.rotation[a.rotIdx].Duration),
		ScreenOff:     a.screenOff,
		Brightness:    a.brightNow,
		UptimeSeconds: int64(time.Since(a.started).Seconds()),
		Now:           time.Now(),
	}

	if s, ok := a.scenes[a.current]; ok {
		st.Kind = s.Kind()
	}

	if a.override != nil {
		o := &Override{Scene: a.override.scene}
		if !a.override.until.IsZero() {
			u := a.override.until
			o.Until = &u
		}

		st.Override = o
	}

	return st
}
