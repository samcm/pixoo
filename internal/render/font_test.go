package render

import "testing"

func TestFontsCoverPrintableASCII(t *testing.T) {
	for r := rune(' '); r <= '~'; r++ {
		if _, ok := Small.glyphs[r]; !ok {
			t.Errorf("small font missing %q", r)
		}
	}

	for r := rune('A'); r <= 'Z'; r++ {
		if _, ok := Tiny.glyphs[r]; !ok {
			t.Errorf("tiny font missing %q", r)
		}
	}

	for r := rune('0'); r <= '9'; r++ {
		if _, ok := Tiny.glyphs[r]; !ok {
			t.Errorf("tiny font missing %q", r)
		}
	}
}

func TestTextWidthAndBounds(t *testing.T) {
	if w := Small.Width("12:34", 2); w != 58 {
		t.Fatalf("big clock width = %d, want 58", w)
	}

	if n := Small.Chars(62, 1); n != 10 {
		t.Fatalf("small chars per line = %d, want 10", n)
	}

	if n := Tiny.Chars(64, 1); n != 16 {
		t.Fatalf("tiny chars per line = %d, want 16", n)
	}

	c := New()
	c.Clear(Black)

	if w := c.Text(-10, 60, "OVERFLOW", Small, White, 3); w <= 0 {
		t.Fatal("text off-canvas should still report a width")
	}

	if got := Wrap("hello there general kenobi", Small, 62, 1); len(got) != 4 {
		t.Fatalf("wrap = %q", got)
	}

	if got := Wrap("supercalifragilistic", Small, 62, 1); len(got) != 2 || got[0] != "supercalif" {
		t.Fatalf("wrap long word = %q", got)
	}
}
