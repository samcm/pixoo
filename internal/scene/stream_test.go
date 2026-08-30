package scene

import (
	"errors"
	"image"
	"image/color"
	"testing"
	"time"
)

func solidFrame(value uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}

	return img
}

func TestStreamBuildsClipAndCoalescesFastFrames(t *testing.T) {
	s := NewStream("stream", StreamOptions{
		MaxFrames:   3,
		FrameDelay:  time.Second,
		FlushAfter:  10 * time.Second,
		SourceLease: time.Minute,
	})
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

	if _, err := s.addFrame(solidFrame(1), "studio", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addFrame(solidFrame(2), "studio", now.Add(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addFrame(solidFrame(3), "studio", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	st, err := s.addFrame(solidFrame(4), "studio", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	if st.Received != 4 || st.Coalesced != 1 || st.ReadyFrames != 3 || st.BuildingFrames != 0 {
		t.Fatalf("status before render = %+v", st)
	}

	frame, _, err := s.Render(t.Context(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frame.Frames))
	}
	if got := frame.Frames[0].RGBAAt(0, 0).R; got != 2 {
		t.Fatalf("first frame value = %d, want coalesced value 2", got)
	}
	if st := s.Status(); st.ClipsBuilt != 1 || st.CurrentFrames != 3 {
		t.Fatalf("status after render = %+v", st)
	}
}

func TestStreamLatestReadyClipWins(t *testing.T) {
	s := NewStream("stream", StreamOptions{
		MaxFrames:   2,
		FrameDelay:  time.Second,
		FlushAfter:  time.Minute,
		SourceLease: time.Minute,
	})
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

	for i, value := range []uint8{1, 2, 3, 4} {
		if _, err := s.addFrame(solidFrame(value), "studio", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	frame, _, err := s.Render(t.Context(), now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Frames) != 2 || frame.Frames[0].RGBAAt(0, 0).R != 3 || frame.Frames[1].RGBAAt(0, 0).R != 4 {
		t.Fatalf("latest ready clip was not retained: %+v", frame.Frames)
	}
	if st := s.Status(); st.DroppedFrames != 2 {
		t.Fatalf("dropped frames = %d, want 2", st.DroppedFrames)
	}
}

func TestStreamKeepsResidentClipUntilMinimumInterval(t *testing.T) {
	s := NewStream("stream", StreamOptions{
		MaxFrames:       2,
		FrameDelay:      time.Second,
		FlushAfter:      time.Minute,
		MinClipInterval: 10 * time.Minute,
		SourceLease:     time.Hour,
	})
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

	for i, value := range []uint8{1, 2} {
		if _, err := s.addFrame(solidFrame(value), "studio", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	first, _, err := s.Render(t.Context(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	for i, value := range []uint8{3, 4} {
		if _, err := s.addFrame(solidFrame(value), "studio", now.Add(time.Minute+time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	stillFirst, next, err := s.Render(t.Context(), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stillFirst.Frames[0].RGBAAt(0, 0).R != first.Frames[0].RGBAAt(0, 0).R {
		t.Fatal("resident clip changed before minimum interval")
	}
	if next <= 0 || next > 8*time.Minute {
		t.Fatalf("next render = %s, want a wake no later than the resident deadline", next)
	}
	if st := s.Status(); st.ReadyFrames != 2 || st.NextClipAt.IsZero() {
		t.Fatalf("pending clip status = %+v", st)
	}

	replacement, _, err := s.Render(t.Context(), now.Add(10*time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := replacement.Frames[0].RGBAAt(0, 0).R; got != 3 {
		t.Fatalf("replacement first frame = %d, want 3", got)
	}
}

func TestStreamExplicitFlushRespectsMinimumInterval(t *testing.T) {
	s := NewStream("stream", StreamOptions{
		MaxFrames:       2,
		FrameDelay:      time.Second,
		FlushAfter:      time.Minute,
		MinClipInterval: time.Hour,
		SourceLease:     time.Hour,
	})
	now := time.Now()

	for i, value := range []uint8{1, 2} {
		if _, err := s.addFrame(solidFrame(value), "studio", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Render(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addFrame(solidFrame(9), "studio", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flush("studio"); err != nil {
		t.Fatal(err)
	}

	resident, _, err := s.Render(t.Context(), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if resident.Frames[0].RGBAAt(0, 0).R != 1 {
		t.Fatal("explicit flush bypassed the minimum resident interval")
	}
	replacement, _, err := s.Render(t.Context(), now.Add(time.Hour+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Image == nil || replacement.Image.RGBAAt(0, 0).R != 9 {
		t.Fatal("flushed partial clip was not installed at the resident deadline")
	}
}

func TestStreamLeaseAndReset(t *testing.T) {
	s := NewStream("stream", StreamOptions{SourceLease: time.Minute})
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

	if _, err := s.addFrame(solidFrame(1), "one", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addFrame(solidFrame(2), "two", now.Add(30*time.Second)); err == nil {
		t.Fatal("expected source lease conflict")
	} else {
		var lease *StreamLeaseError
		if !errors.As(err, &lease) || lease.Holder != "one" {
			t.Fatalf("lease error = %v", err)
		}
	}

	if _, err := s.addFrame(solidFrame(3), "two", now.Add(61*time.Second)); err != nil {
		t.Fatalf("expired lease should be claimable: %v", err)
	}
	if st := s.Status(); st.Source != "two" || st.DroppedFrames != 1 {
		t.Fatalf("status after takeover = %+v", st)
	}
	frame, _, err := s.Render(t.Context(), now.Add(61*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Image == nil || frame.Image.RGBAAt(0, 0).R != 3 {
		t.Fatal("new producer did not replace the previous producer promptly")
	}
}

func TestStreamFlushesPartialClip(t *testing.T) {
	s := NewStream("stream", StreamOptions{MaxFrames: 30, FrameDelay: time.Second, FlushAfter: 2 * time.Second})
	now := time.Now()

	if _, err := s.addFrame(solidFrame(1), "studio", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addFrame(solidFrame(2), "studio", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	frame, _, err := s.Render(t.Context(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Frames) != 2 {
		t.Fatalf("frames = %d, want timed partial clip of 2", len(frame.Frames))
	}
}

func TestStreamClearDropsStateAcrossBlackout(t *testing.T) {
	s := NewStream("stream", StreamOptions{MaxFrames: 2, FrameDelay: time.Second})
	now := time.Now()

	if _, err := s.addFrame(solidFrame(1), "studio", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addFrame(solidFrame(2), "studio", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Render(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	s.Clear()
	st := s.Status()
	if st.Source != "" || st.BuildingFrames != 0 || st.ReadyFrames != 0 || st.CurrentFrames != 0 {
		t.Fatalf("status after clear = %+v", st)
	}
}
