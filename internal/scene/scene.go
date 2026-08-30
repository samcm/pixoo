// Package scene holds the things the display can show. A scene renders a
// frame for a moment in time and says when it wants to be asked again.
package scene

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samcm/pixoo/internal/beacon"
	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/render"
)

// Frame is one render result. Frames (an animation the device loops on its
// own) takes precedence over Image; ScreenOff asks for the panel to go dark.
type Frame struct {
	Image     *image.RGBA
	Frames    []*image.RGBA
	Delay     time.Duration
	ScreenOff bool
}

// Preview is the single image to show in the UI for this frame.
func (f Frame) Preview() *image.RGBA {
	if len(f.Frames) > 0 {
		return f.Frames[0]
	}

	return f.Image
}

type Scene interface {
	Name() string
	Kind() string
	// Render draws the scene at now and returns how long until it should be
	// rendered again; zero means the caller's default.
	Render(ctx context.Context, now time.Time) (Frame, time.Duration, error)
}

type WeatherSource interface {
	Weather(ctx context.Context) (pixoo.Weather, error)
}

type Deps struct {
	Weather            WeatherSource
	Beacon             *beacon.Client
	Validators         []uint64
	Location           *time.Location
	Logger             *slog.Logger
	AnimationMaxFrames int
	AnimationMinUpdate time.Duration
}

type Factory func(name string, opts map[string]any, deps Deps) (Scene, error)

var factories = map[string]Factory{
	"clock":  newClock,
	"text":   newText,
	"image":  newImage,
	"beacon": newBeacon,
	"off":    newOff,
}

func New(kind, name string, opts map[string]any, deps Deps) (Scene, error) {
	f, ok := factories[kind]
	if !ok {
		return nil, fmt.Errorf("scene: unknown kind %q (have %s)", kind, strings.Join(Kinds(), ", "))
	}

	if deps.Location == nil {
		deps.Location = time.Local
	}

	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	return f(name, opts, deps)
}

func Kinds() []string {
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func optString(opts map[string]any, key, def string) string {
	if v, ok := opts[key]; ok {
		return fmt.Sprint(v)
	}

	return def
}

func optBool(opts map[string]any, key string, def bool) bool {
	v, ok := opts[key]
	if !ok {
		return def
	}

	switch b := v.(type) {
	case bool:
		return b
	case string:
		p, err := strconv.ParseBool(b)
		if err != nil {
			return def
		}

		return p
	}

	return def
}

func optInt(opts map[string]any, key string, def int) int {
	v, ok := opts[key]
	if !ok {
		return def
	}

	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		p, err := strconv.Atoi(n)
		if err != nil {
			return def
		}

		return p
	}

	return def
}

func optColor(opts map[string]any, key string, def color.RGBA) color.RGBA {
	v, ok := opts[key]
	if !ok {
		return def
	}

	c, err := ParseColor(fmt.Sprint(v))
	if err != nil {
		return def
	}

	return c
}

var named = map[string]color.RGBA{
	"white": render.White, "black": render.Black, "grey": render.Grey, "gray": render.Grey,
	"red": render.Red, "orange": render.Orange, "yellow": render.Yellow, "green": render.Green,
	"cyan": render.Cyan, "blue": render.Blue, "purple": render.Purple, "pink": render.Pink,
}

// ParseColor accepts a named colour or #rgb / #rrggbb.
func ParseColor(s string) (color.RGBA, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := named[s]; ok {
		return c, nil
	}

	s = strings.TrimPrefix(s, "#")

	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	case 6:
	default:
		return color.RGBA{}, fmt.Errorf("bad colour %q", s)
	}

	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("bad colour %q", s)
	}

	return color.RGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}, nil
}

func fontByName(name string) (*render.Font, int) {
	switch strings.ToLower(name) {
	case "tiny":
		return render.Tiny, 1
	case "big", "large":
		return render.Small, 2
	default:
		return render.Small, 1
	}
}

type off struct{ name string }

func newOff(name string, _ map[string]any, _ Deps) (Scene, error) {
	return &off{name: name}, nil
}

func (o *off) Name() string { return o.name }
func (o *off) Kind() string { return "off" }

func (o *off) Render(context.Context, time.Time) (Frame, time.Duration, error) {
	return Frame{ScreenOff: true}, time.Minute, nil
}
