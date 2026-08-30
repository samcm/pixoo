package scene

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"testing"
	"time"
)

func TestImageSceneCapsAnimationAtConfiguredSafeLimit(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	frames := make([]*image.Paletted, 12)
	delays := make([]int, len(frames))
	for index := range frames {
		frames[index] = image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
		frames[index].Pix[0] = uint8(index % 2)
		delays[index] = 10
	}
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, &gif.GIF{Image: frames, Delay: delays}); err != nil {
		t.Fatal(err)
	}

	value, err := New("image", "image", nil, Deps{AnimationMaxFrames: 8})
	if err != nil {
		t.Fatal(err)
	}
	imageScene := value.(*Image)
	if err := imageScene.Set(encoded.Bytes(), "test.gif"); err != nil {
		t.Fatal(err)
	}
	frame, _, err := imageScene.Render(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(frame.Frames); got != 8 {
		t.Fatalf("resident frames = %d, want configured cap 8", got)
	}
}

func TestImageSceneKeepsResidentAnimationUntilMinimumInterval(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	value, err := New("image", "image", nil, Deps{AnimationMaxFrames: 8, AnimationMinUpdate: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	imageScene := value.(*Image)
	encode := func(pixel color.RGBA) []byte {
		var encoded bytes.Buffer
		frame := image.NewRGBA(image.Rect(0, 0, 2, 2))
		frame.SetRGBA(0, 0, pixel)
		if err := gif.Encode(&encoded, frame, nil); err != nil {
			t.Fatal(err)
		}
		return encoded.Bytes()
	}

	if err := imageScene.Set(encode(color.RGBA{R: 1, A: 255}), "first.gif"); err != nil {
		t.Fatal(err)
	}
	first, _, err := imageScene.Render(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := imageScene.Set(encode(color.RGBA{R: 200, A: 255}), "second.gif"); err != nil {
		t.Fatal(err)
	}
	resident, next, err := imageScene.Render(t.Context(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if resident.Image.RGBAAt(0, 0) != first.Image.RGBAAt(0, 0) {
		t.Fatal("resident image changed before minimum interval")
	}
	if next < 28*time.Minute || next > 29*time.Minute {
		t.Fatalf("next render = %s, want the resident deadline", next)
	}
	replacement, _, err := imageScene.Render(t.Context(), now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got := replacement.Image.RGBAAt(0, 0).R; got < 150 {
		t.Fatalf("replacement red = %d, want second image", got)
	}
}
