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
