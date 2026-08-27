package scene

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/render"
)

type fakeWeather struct{}

func (fakeWeather) Weather(context.Context) (pixoo.Weather, error) {
	return pixoo.Weather{Condition: "Cloudy", Temp: 24.03, MinTemp: 18, MaxTemp: 27, Humidity: 87}, nil
}

// TestSheets renders the fonts and every offline scene. Set PIXOO_SHEET_DIR to
// also write the frames as scaled PNGs for a visual check.
func TestSheets(t *testing.T) {
	dir := os.Getenv("PIXOO_SHEET_DIR")
	write := func(name string, img *render.Canvas) {
		if dir == "" {
			return
		}

		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		if err := png.Encode(f, render.Scale(img.Img, 6)); err != nil {
			t.Fatal(err)
		}
	}

	fonts := render.New()
	fonts.Clear(render.Black)
	fonts.Text(1, 1, "ABCDEFGHIJ", render.Small, render.White, 1)
	fonts.Text(1, 9, "KLMNOPQRST", render.Small, render.White, 1)
	fonts.Text(1, 17, "UVWXYZ0123", render.Small, render.White, 1)
	fonts.Text(1, 25, "456789:.-/", render.Small, render.Yellow, 1)
	fonts.Text(1, 33, "abcdefghij", render.Small, render.Cyan, 1)
	fonts.Text(1, 41, "klmnopqrst", render.Small, render.Cyan, 1)
	fonts.Text(1, 49, "uvwxyz!?%#", render.Small, render.Cyan, 1)
	fonts.Text(1, 57, "ABCDEFGHIJKLMNOP", render.Tiny, render.Green, 1)
	write("font-small", fonts)

	tiny := render.New()
	tiny.Clear(render.Black)
	tiny.Text(1, 1, "ABCDEFGHIJKLMNOP", render.Tiny, render.White, 1)
	tiny.Text(1, 8, "QRSTUVWXYZ012345", render.Tiny, render.White, 1)
	tiny.Text(1, 15, "6789:.-/%+!?',#(", render.Tiny, render.Yellow, 1)
	tiny.Text(1, 22, ")=<>_*° THU 27 AUG", render.Tiny, render.Green, 1)
	tiny.Text(1, 30, "12:34", render.Small, render.White, 2)
	tiny.Text(1, 48, "&@$[]{}~^\\|;\"`", render.Small, render.Pink, 1)
	write("font-tiny", tiny)

	deps := Deps{Weather: fakeWeather{}, Location: time.UTC}
	now := time.Date(2026, 8, 27, 19, 42, 0, 0, time.UTC)

	cases := map[string]struct {
		kind string
		opts map[string]any
	}{
		"clock-24h":    {"clock", nil},
		"clock-12h":    {"clock", map[string]any{"format": "12h"}},
		"clock-bare":   {"clock", map[string]any{"date": false, "weather": false}},
		"text-wrap":    {"text", map[string]any{"text": "hello there general kenobi", "color": "yellow"}},
		"text-big":     {"text", map[string]any{"text": "GM", "font": "big", "color": "#ff77aa"}},
		"text-tiny":    {"text", map[string]any{"text": "THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG 0123456789", "font": "tiny"}},
		"text-marquee": {"text", map[string]any{"text": "scrolling marquee text here", "scroll": true}},
	}

	for name, tc := range cases {
		sc, err := New(tc.kind, name, tc.opts, deps)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		frame, _, err := sc.Render(context.Background(), now)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		img := frame.Preview()
		if img == nil || img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
			t.Fatalf("%s: bad frame", name)
		}

		if len(frame.Frames) > pixoo.MaxFrames {
			t.Fatalf("%s: %d frames", name, len(frame.Frames))
		}

		write(name, &render.Canvas{Img: img})

		if name == "text-marquee" {
			for i, f := range frame.Frames {
				if i%15 == 0 {
					write("text-marquee-"+string(rune('a'+i/15)), &render.Canvas{Img: f})
				}
			}
		}
	}
}
