// Package render draws 64x64 frames for the Pixoo with bitmap fonts.
package render

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	xdraw "golang.org/x/image/draw"
)

const Size = 64

var (
	Black  = color.RGBA{0, 0, 0, 255}
	White  = color.RGBA{255, 255, 255, 255}
	Grey   = color.RGBA{110, 110, 120, 255}
	Dim    = color.RGBA{40, 40, 48, 255}
	Red    = color.RGBA{230, 60, 60, 255}
	Orange = color.RGBA{255, 150, 40, 255}
	Yellow = color.RGBA{250, 210, 50, 255}
	Green  = color.RGBA{60, 210, 90, 255}
	Cyan   = color.RGBA{70, 200, 230, 255}
	Blue   = color.RGBA{80, 120, 255, 255}
	Purple = color.RGBA{170, 90, 240, 255}
	Pink   = color.RGBA{255, 100, 180, 255}
)

type Canvas struct {
	Img *image.RGBA
}

func New() *Canvas {
	return &Canvas{Img: image.NewRGBA(image.Rect(0, 0, Size, Size))}
}

func (c *Canvas) Clear(col color.RGBA) {
	draw.Draw(c.Img, c.Img.Bounds(), &image.Uniform{col}, image.Point{}, draw.Src)
}

func (c *Canvas) Set(x, y int, col color.RGBA) {
	if x < 0 || y < 0 || x >= Size || y >= Size {
		return
	}

	c.Img.SetRGBA(x, y, col)
}

func (c *Canvas) HLine(x0, x1, y int, col color.RGBA) {
	for x := x0; x <= x1; x++ {
		c.Set(x, y, col)
	}
}

func (c *Canvas) VLine(x, y0, y1 int, col color.RGBA) {
	for y := y0; y <= y1; y++ {
		c.Set(x, y, col)
	}
}

func (c *Canvas) FillRect(x, y, w, h int, col color.RGBA) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			c.Set(xx, yy, col)
		}
	}
}

func (c *Canvas) Rect(x, y, w, h int, col color.RGBA) {
	c.HLine(x, x+w-1, y, col)
	c.HLine(x, x+w-1, y+h-1, col)
	c.VLine(x, y, y+h-1, col)
	c.VLine(x+w-1, y, y+h-1, col)
}

// Bar draws a horizontal progress bar filled to frac (0..1).
func (c *Canvas) Bar(x, y, w, h int, frac float64, fg, bg color.RGBA) {
	if frac < 0 {
		frac = 0
	}

	if frac > 1 {
		frac = 1
	}

	c.FillRect(x, y, w, h, bg)
	c.FillRect(x, y, int(float64(w)*frac+0.5), h, fg)
}

func (c *Canvas) Draw(img image.Image, x, y int) {
	r := img.Bounds()
	draw.Draw(c.Img, image.Rect(x, y, x+r.Dx(), y+r.Dy()), img, r.Min, draw.Over)
}

// RGB returns the packed 24-bit frame the device expects.
func (c *Canvas) RGB() []byte {
	return RGB(c.Img)
}

func RGB(img *image.RGBA) []byte {
	out := make([]byte, 0, Size*Size*3)

	for y := 0; y < Size; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+Size*4]
		for x := 0; x < Size*4; x += 4 {
			out = append(out, row[x], row[x+1], row[x+2])
		}
	}

	return out
}

// Scale returns img enlarged by an integer factor with hard pixel edges.
func Scale(img *image.RGBA, factor int) *image.RGBA {
	if factor < 1 {
		factor = 1
	}

	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*factor, b.Dy()*factor))
	xdraw.NearestNeighbor.Scale(out, out.Bounds(), img, b, draw.Src, nil)

	return out
}

func EncodePNG(img *image.RGBA, factor int) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, Scale(img, factor)); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// Fit scales img to fill 64x64, keeping its aspect ratio and letterboxing
// the remainder in black.
func Fit(img image.Image) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, Size, Size))
	b := img.Bounds()

	if b.Dx() == 0 || b.Dy() == 0 {
		return out
	}

	w, h := Size, Size
	if b.Dx() > b.Dy() {
		h = Size * b.Dy() / b.Dx()
	} else if b.Dy() > b.Dx() {
		w = Size * b.Dx() / b.Dy()
	}

	dst := image.Rect((Size-w)/2, (Size-h)/2, (Size-w)/2+w, (Size-h)/2+h)

	scaler := xdraw.Interpolator(xdraw.CatmullRom)
	if b.Dx() <= Size && b.Dy() <= Size {
		scaler = xdraw.NearestNeighbor
	}

	scaler.Scale(out, dst, img, b, draw.Src, nil)

	return out
}

func Copy(img *image.RGBA) *image.RGBA {
	out := image.NewRGBA(img.Bounds())
	copy(out.Pix, img.Pix)

	return out
}
