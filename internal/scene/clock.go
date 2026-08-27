package scene

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/render"
)

type clock struct {
	name    string
	loc     *time.Location
	source  WeatherSource
	hour24  bool
	date    bool
	weather bool
	colour  color.RGBA
	accent  color.RGBA

	mu        sync.Mutex
	cached    pixoo.Weather
	fetchedAt time.Time
	haveCache bool
}

func newClock(name string, opts map[string]any, deps Deps) (Scene, error) {
	return &clock{
		name:    name,
		loc:     deps.Location,
		source:  deps.Weather,
		hour24:  optString(opts, "format", "24h") != "12h",
		date:    optBool(opts, "date", true),
		weather: optBool(opts, "weather", true) && deps.Weather != nil,
		colour:  optColor(opts, "color", render.White),
		accent:  optColor(opts, "accent", render.Cyan),
	}, nil
}

func (c *clock) Name() string { return c.name }
func (c *clock) Kind() string { return "clock" }

func (c *clock) Render(ctx context.Context, now time.Time) (Frame, time.Duration, error) {
	now = now.In(c.loc)
	cv := render.New()
	cv.Clear(render.Black)

	hour := now.Hour()
	suffix := ""

	if !c.hour24 {
		suffix = "AM"
		if hour >= 12 {
			suffix = "PM"
		}

		hour %= 12
		if hour == 0 {
			hour = 12
		}
	}

	timeStr := fmt.Sprintf("%02d:%02d", hour, now.Minute())
	if !c.hour24 {
		timeStr = fmt.Sprintf("%d:%02d", hour, now.Minute())
	}

	y := 8
	if !c.date && !c.weather {
		y = 25
	}

	cv.TextCentered(y, timeStr, render.Small, c.colour, 2)

	if suffix != "" {
		cv.Text(64-render.Tiny.Width(suffix, 1)-1, y+15, suffix, render.Tiny, render.Grey, 1)
	}

	line := y + 20

	if c.date {
		cv.HLine(6, 57, line-3, render.Dim)
		cv.TextCentered(line, strings.ToUpper(now.Format("Mon 2 Jan")), render.Tiny, c.accent, 1)
		line += 9
	}

	if c.weather {
		w, ok := c.currentWeather(ctx)
		if ok {
			temp := fmt.Sprintf("%.0f°", w.Temp)
			cond := strings.ToUpper(w.Condition)

			if render.Tiny.Width(temp+" "+cond, 1) <= 62 {
				cv.TextCentered(line+2, temp+" "+cond, render.Tiny, render.Yellow, 1)
			} else {
				cv.TextCentered(line+2, temp, render.Tiny, render.Yellow, 1)
				cv.TextCentered(line+10, cond, render.Tiny, render.Yellow, 1)
			}

			line += 10
			cv.TextCentered(line+7, fmt.Sprintf("%.0f°/%.0f° %d%%", w.MinTemp, w.MaxTemp, w.Humidity), render.Tiny, render.Grey, 1)
		}
	}

	next := time.Until(now.Truncate(time.Minute).Add(time.Minute)) + 100*time.Millisecond

	return Frame{Image: cv.Img}, next, nil
}

func (c *clock) currentWeather(ctx context.Context) (pixoo.Weather, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.haveCache && time.Since(c.fetchedAt) < 10*time.Minute {
		return c.cached, true
	}

	w, err := c.source.Weather(ctx)
	if err != nil {
		return c.cached, c.haveCache
	}

	c.cached = w
	c.fetchedAt = time.Now()
	c.haveCache = true

	return w, true
}
