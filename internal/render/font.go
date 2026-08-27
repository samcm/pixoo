package render

import (
	"image/color"
	"strings"
	"unicode"
)

// Font is a fixed-width bitmap font. Each glyph row is a bitmask with the
// leftmost pixel in the most significant of W bits.
type Font struct {
	Name   string
	W, H   int
	Gap    int
	glyphs map[rune][]uint8
	upper  bool
}

func newFont(name string, w, h, gap int, upper bool, art map[rune][]string) *Font {
	f := &Font{Name: name, W: w, H: h, Gap: gap, glyphs: make(map[rune][]uint8, len(art)), upper: upper}

	for r, rows := range art {
		if len(rows) != h {
			panic("font " + name + ": glyph " + string(r) + " has wrong height")
		}

		g := make([]uint8, h)

		for y, row := range rows {
			if len(row) != w {
				panic("font " + name + ": glyph " + string(r) + " has wrong width")
			}

			for x := 0; x < w; x++ {
				if row[x] != '.' {
					g[y] |= 1 << uint(w-1-x)
				}
			}
		}

		f.glyphs[r] = g
	}

	return f
}

func (f *Font) glyph(r rune) ([]uint8, bool) {
	if g, ok := f.glyphs[r]; ok {
		return g, true
	}

	if f.upper {
		if g, ok := f.glyphs[unicode.ToUpper(r)]; ok {
			return g, true
		}
	}

	g, ok := f.glyphs['?']

	return g, ok
}

// Advance is the horizontal step per character.
func (f *Font) Advance(scale int) int {
	return (f.W + f.Gap) * scale
}

func (f *Font) LineHeight(scale int) int {
	return (f.H + 1) * scale
}

func (f *Font) Width(s string, scale int) int {
	n := len([]rune(s))
	if n == 0 {
		return 0
	}

	return n*f.Advance(scale) - f.Gap*scale
}

// Chars is how many characters fit in width px.
func (f *Font) Chars(width, scale int) int {
	return (width + f.Gap*scale) / f.Advance(scale)
}

// Text draws s with its top-left corner at (x, y) and returns the width drawn.
func (c *Canvas) Text(x, y int, s string, f *Font, col color.RGBA, scale int) int {
	if scale < 1 {
		scale = 1
	}

	start := x

	for _, r := range s {
		g, ok := f.glyph(r)
		if ok {
			for gy, row := range g {
				for gx := 0; gx < f.W; gx++ {
					if row&(1<<uint(f.W-1-gx)) != 0 {
						c.FillRect(x+gx*scale, y+gy*scale, scale, scale, col)
					}
				}
			}
		}

		x += f.Advance(scale)
	}

	return x - start - f.Gap*scale
}

func (c *Canvas) TextCentered(y int, s string, f *Font, col color.RGBA, scale int) int {
	x := (Size - f.Width(s, scale)) / 2

	return c.Text(x, y, s, f, col, scale)
}

func (c *Canvas) TextRight(right, y int, s string, f *Font, col color.RGBA, scale int) int {
	return c.Text(right-f.Width(s, scale), y, s, f, col, scale)
}

// Wrap splits s into lines of at most width px, breaking on spaces and
// hard-splitting words that do not fit on their own.
func Wrap(s string, f *Font, width, scale int) []string {
	max := f.Chars(width, scale)
	if max < 1 {
		max = 1
	}

	var lines []string

	for _, para := range strings.Split(s, "\n") {
		line := ""

		for _, word := range strings.Fields(para) {
			for len([]rune(word)) > max {
				if line != "" {
					lines = append(lines, line)
					line = ""
				}

				rs := []rune(word)
				lines = append(lines, string(rs[:max]))
				word = string(rs[max:])
			}

			switch {
			case line == "":
				line = word
			case len([]rune(line))+1+len([]rune(word)) <= max:
				line += " " + word
			default:
				lines = append(lines, line)
				line = word
			}
		}

		lines = append(lines, line)
	}

	return lines
}
