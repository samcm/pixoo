package scene

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"sync"
	"time"

	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/render"
)

type Image struct {
	name        string
	maxFrames   int
	minInterval time.Duration

	mu            sync.Mutex
	frame         Frame
	ready         Frame
	label         string
	readyLabel    string
	lastActivated time.Time
}

func newImage(name string, opts map[string]any, deps Deps) (Scene, error) {
	maxFrames := deps.AnimationMaxFrames
	if maxFrames <= 0 || maxFrames > pixoo.MaxFrames {
		maxFrames = pixoo.MaxFrames
	}
	img := &Image{name: name, maxFrames: maxFrames, minInterval: deps.AnimationMinUpdate}

	if path := optString(opts, "path", ""); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("scene %s: %w", name, err)
		}

		if err := img.Set(data, path); err != nil {
			return nil, fmt.Errorf("scene %s: %w", name, err)
		}
	}

	return img, nil
}

func (i *Image) Name() string { return i.name }
func (i *Image) Kind() string { return "image" }

func (i *Image) Label() string {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.label
}

// Set decodes a PNG, JPEG or GIF. Animated GIFs become device-side animations.
func (i *Image) Set(data []byte, label string) error {
	frame, err := decode(data, i.maxFrames)
	if err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	i.ready = frame
	i.readyLabel = label

	return nil
}

func (i *Image) Render(_ context.Context, now time.Time) (Frame, time.Duration, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	hasCurrent := i.frame.Image != nil || len(i.frame.Frames) > 0
	hasReady := i.ready.Image != nil || len(i.ready.Frames) > 0
	if hasReady && (!hasCurrent || i.minInterval <= 0 || !now.Before(i.lastActivated.Add(i.minInterval))) {
		i.frame = i.ready
		i.ready = Frame{}
		i.label = i.readyLabel
		i.readyLabel = ""
		i.lastActivated = now
		hasCurrent = true
		hasReady = false
	}
	if !hasCurrent {
		return Frame{}, 0, fmt.Errorf("scene %s: no image loaded", i.name)
	}

	next := time.Minute
	if hasReady {
		next = max(i.lastActivated.Add(i.minInterval).Sub(now), 50*time.Millisecond)
	}
	return i.frame, next, nil
}

func Decode(data []byte) (Frame, error) {
	return decode(data, pixoo.MaxFrames)
}

func decode(data []byte, maxFrames int) (Frame, error) {
	if bytes.HasPrefix(data, []byte("GIF8")) {
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return Frame{}, err
		}

		if len(g.Image) > 1 {
			return decodeAnimation(g, maxFrames), nil
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Frame{}, err
	}

	return Frame{Image: render.Fit(img)}, nil
}

func decodeAnimation(g *gif.GIF, maxFrames int) Frame {
	bounds := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if bounds.Empty() {
		bounds = g.Image[0].Bounds()
	}

	canvas := image.NewRGBA(bounds)
	frames := make([]*image.RGBA, 0, len(g.Image))
	total := 0

	for idx, src := range g.Image {
		draw.Draw(canvas, src.Bounds(), src, src.Bounds().Min, draw.Over)
		frames = append(frames, render.Fit(canvas))

		delay := 10
		if idx < len(g.Delay) && g.Delay[idx] > 0 {
			delay = g.Delay[idx]
		}

		total += delay

		if idx < len(g.Disposal) && g.Disposal[idx] == gif.DisposalBackground {
			draw.Draw(canvas, src.Bounds(), image.Transparent, image.Point{}, draw.Src)
		}
	}

	if len(frames) > maxFrames {
		stride := float64(len(frames)) / float64(maxFrames)
		picked := make([]*image.RGBA, 0, maxFrames)

		for n := 0; n < maxFrames; n++ {
			picked = append(picked, frames[int(float64(n)*stride)])
		}

		frames = picked
	}

	avg := time.Duration(total*10/len(g.Image)) * time.Millisecond
	if len(frames) < len(g.Image) {
		avg = time.Duration(total*10/len(frames)) * time.Millisecond
	}

	return Frame{Frames: frames, Delay: avg}
}
