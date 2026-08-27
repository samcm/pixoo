package scene

import (
	"context"
	"image"
	"image/color"
	"sync"
	"time"

	"github.com/samcm/pixoo/internal/render"
)

type TextOptions struct {
	Text       string
	Color      color.RGBA
	Background color.RGBA
	Font       string
	Scroll     bool
}

type Text struct {
	name string

	mu   sync.Mutex
	opts TextOptions
}

func newText(name string, opts map[string]any, _ Deps) (Scene, error) {
	return &Text{
		name: name,
		opts: TextOptions{
			Text:       optString(opts, "text", ""),
			Color:      optColor(opts, "color", render.White),
			Background: optColor(opts, "background", render.Black),
			Font:       optString(opts, "font", "small"),
			Scroll:     optBool(opts, "scroll", false),
		},
	}, nil
}

func (t *Text) Name() string { return t.name }
func (t *Text) Kind() string { return "text" }

func (t *Text) Set(opts TextOptions) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.opts = opts
}

func (t *Text) Options() TextOptions {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.opts
}

func (t *Text) Render(_ context.Context, _ time.Time) (Frame, time.Duration, error) {
	o := t.Options()
	font, scale := fontByName(o.Font)

	if o.Scroll && render.Size < font.Width(o.Text, scale) {
		return t.marquee(o, font, scale), time.Minute, nil
	}

	cv := render.New()
	cv.Clear(o.Background)

	lines := render.Wrap(o.Text, font, render.Size-2, scale)
	lh := font.LineHeight(scale)

	maxLines := render.Size / lh
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	top := (render.Size - len(lines)*lh + 1) / 2

	for i, line := range lines {
		cv.TextCentered(top+i*lh, line, font, o.Color, scale)
	}

	return Frame{Image: cv.Img}, time.Minute, nil
}

// marquee pre-renders the scroll as an animation so the device plays it
// smoothly on its own instead of us pushing a frame per step.
func (t *Text) marquee(o TextOptions, font *render.Font, scale int) Frame {
	width := font.Width(o.Text, scale)
	travel := width + render.Size

	step := (travel + 59) / 60
	if step < 1 {
		step = 1
	}

	y := (render.Size - font.H*scale) / 2
	frames := make([]*image.RGBA, 0, 60)

	for off := 0; off < travel; off += step {
		cv := render.New()
		cv.Clear(o.Background)
		cv.Text(render.Size-off, y, o.Text, font, o.Color, scale)
		frames = append(frames, cv.Img)
	}

	return Frame{Frames: frames, Delay: 120 * time.Millisecond}
}
