package scene

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/samcm/pixoo/internal/pixoo"
)

const anonymousStreamSource = "anonymous"

type StreamOptions struct {
	MaxFrames       int
	FrameDelay      time.Duration
	FlushAfter      time.Duration
	MinClipInterval time.Duration
	SourceLease     time.Duration
}

func (o *StreamOptions) setDefaults() {
	if o.MaxFrames <= 0 {
		o.MaxFrames = 30
	}
	if o.MaxFrames > pixoo.MaxFrames {
		o.MaxFrames = pixoo.MaxFrames
	}
	if o.FrameDelay <= 0 {
		o.FrameDelay = time.Second
	}
	if o.FlushAfter <= 0 {
		o.FlushAfter = 30 * time.Second
	}
	if o.MinClipInterval <= 0 {
		o.MinClipInterval = 5 * time.Minute
	}
	if o.SourceLease <= 0 {
		o.SourceLease = 2 * time.Minute
	}
}

// StreamLeaseError means another producer currently owns the stream.
type StreamLeaseError struct {
	Holder string
	Until  time.Time
}

func (e *StreamLeaseError) Error() string {
	return fmt.Sprintf("stream is leased by %q until %s", e.Holder, e.Until.Format(time.RFC3339))
}

type StreamStatus struct {
	Source          string        `json:"source,omitempty"`
	LeaseUntil      time.Time     `json:"lease_until,omitempty"`
	MaxFrames       int           `json:"max_frames"`
	FrameDelay      time.Duration `json:"frame_delay"`
	FlushAfter      time.Duration `json:"flush_after"`
	MinClipInterval time.Duration `json:"min_clip_interval"`
	Received        uint64        `json:"received"`
	Coalesced       uint64        `json:"coalesced"`
	DroppedFrames   uint64        `json:"dropped_frames"`
	ClipsBuilt      uint64        `json:"clips_built"`
	BuildingFrames  int           `json:"building_frames"`
	ReadyFrames     int           `json:"ready_frames"`
	CurrentFrames   int           `json:"current_frames"`
	FirstFrameAt    time.Time     `json:"first_frame_at,omitempty"`
	LastFrameAt     time.Time     `json:"last_frame_at,omitempty"`
	NextFlushAt     time.Time     `json:"next_flush_at,omitempty"`
	NextClipAt      time.Time     `json:"next_clip_at,omitempty"`
	LastClipBuiltAt time.Time     `json:"last_clip_built_at,omitempty"`
}

// Stream buffers individual images into clips which the panel can loop
// locally. It keeps one building clip and one ready clip; when producers outrun
// the display, the newest complete clip replaces the older ready one.
type Stream struct {
	name string
	opts StreamOptions

	mu sync.Mutex

	source     string
	leaseUntil time.Time
	received   uint64
	coalesced  uint64
	dropped    uint64
	clipsBuilt uint64

	building      []*image.RGBA
	buildingStart time.Time
	lastFrameAt   time.Time
	lastSlot      int
	ready         []*image.RGBA
	readyForced   bool
	current       []*image.RGBA
	lastClipAt    time.Time
}

func NewStream(name string, opts StreamOptions) *Stream {
	opts.setDefaults()

	return &Stream{name: name, opts: opts, lastSlot: -1}
}

func (s *Stream) Name() string { return s.name }
func (s *Stream) Kind() string { return "stream" }

func (s *Stream) Add(data []byte, source string) (StreamStatus, error) {
	frame, err := Decode(data)
	if err != nil {
		return StreamStatus{}, err
	}
	if len(frame.Frames) > 0 {
		return StreamStatus{}, errors.New("stream frames must be still images; upload animated GIFs to /api/image")
	}

	return s.addFrame(frame.Image, source, time.Now())
}

func (s *Stream) addFrame(frame *image.RGBA, source string, now time.Time) (StreamStatus, error) {
	if frame == nil {
		return StreamStatus{}, errors.New("stream frame is empty")
	}
	if source == "" {
		source = anonymousStreamSource
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.source != "" && s.source != source && now.Before(s.leaseUntil) {
		return s.statusLocked(now), &StreamLeaseError{Holder: s.source, Until: s.leaseUntil}
	}

	if s.source != source {
		s.dropPendingLocked()
		s.current = nil
		s.source = source
	}

	s.leaseUntil = now.Add(s.opts.SourceLease)
	s.received++
	s.lastFrameAt = now

	if len(s.building) == 0 {
		s.buildingStart = now
		s.lastSlot = 0
		s.building = append(s.building, frame)
	} else {
		slot := int(now.Sub(s.buildingStart) / s.opts.FrameDelay)
		if slot <= s.lastSlot {
			s.building[len(s.building)-1] = frame
			s.coalesced++
		} else {
			s.building = append(s.building, frame)
			s.lastSlot = slot
		}
	}

	if len(s.building) >= s.opts.MaxFrames {
		s.queueLocked(false)
	}

	return s.statusLocked(now), nil
}

// Flush closes the current partial clip. The caller should wake the display
// loop after a successful flush.
func (s *Stream) Flush(source string) (StreamStatus, error) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkOwnerLocked(source, now); err != nil {
		return s.statusLocked(now), err
	}

	s.queueLocked(true)

	return s.statusLocked(now), nil
}

// Reset releases the source lease and discards buffered and active clips.
func (s *Stream) Reset(source string) (StreamStatus, error) {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkOwnerLocked(source, now); err != nil {
		return s.statusLocked(now), err
	}

	s.clearLocked()

	return s.statusLocked(now), nil
}

// Clear unconditionally releases the producer and all clip state. It is used
// when the panel is turned off so a partial clip cannot leak across a blackout.
func (s *Stream) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.clearLocked()
}

func (s *Stream) Status() StreamStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.statusLocked(time.Now())
}

func (s *Stream) Render(_ context.Context, now time.Time) (Frame, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.building) > 0 && !now.Before(s.buildingStart.Add(s.opts.FlushAfter)) {
		s.queueLocked(false)
	}

	clipDue := len(s.current) == 0 || s.readyForced || !now.Before(s.lastClipAt.Add(s.opts.MinClipInterval))
	if len(s.ready) > 0 && clipDue {
		s.current = s.ready
		s.ready = nil
		s.readyForced = false
		s.clipsBuilt++
		s.lastClipAt = now
	}

	frames := s.current
	if len(frames) == 0 && len(s.building) > 0 {
		// Put the first frame on screen promptly, then leave it untouched while
		// the rest of the first clip is collected.
		frames = s.building[:1]
	}
	if len(frames) == 0 {
		return Frame{}, time.Minute, errors.New("stream has no frames")
	}

	next := time.Minute
	if len(s.building) > 0 {
		next = max(s.buildingStart.Add(s.opts.FlushAfter).Sub(now), 50*time.Millisecond)
	}
	if len(s.ready) > 0 && len(s.current) > 0 && !s.readyForced {
		untilClip := max(s.lastClipAt.Add(s.opts.MinClipInterval).Sub(now), 50*time.Millisecond)
		if untilClip < next {
			next = untilClip
		}
	}

	if len(frames) == 1 {
		return Frame{Image: frames[0]}, next, nil
	}

	return Frame{Frames: frames, Delay: s.opts.FrameDelay}, next, nil
}

func (s *Stream) checkOwnerLocked(source string, now time.Time) error {
	if source == "" {
		source = anonymousStreamSource
	}
	if s.source != "" && s.source != source && now.Before(s.leaseUntil) {
		return &StreamLeaseError{Holder: s.source, Until: s.leaseUntil}
	}

	return nil
}

func (s *Stream) queueLocked(force bool) {
	if len(s.building) == 0 {
		return
	}
	if len(s.ready) > 0 {
		s.dropped += uint64(len(s.ready))
	}

	s.ready = s.building
	s.readyForced = s.readyForced || force
	s.building = nil
	s.buildingStart = time.Time{}
	s.lastSlot = -1
}

func (s *Stream) dropPendingLocked() {
	s.dropped += uint64(len(s.building) + len(s.ready))
	s.building = nil
	s.ready = nil
	s.readyForced = false
	s.buildingStart = time.Time{}
	s.lastSlot = -1
}

func (s *Stream) clearLocked() {
	s.dropPendingLocked()
	s.current = nil
	s.source = ""
	s.leaseUntil = time.Time{}
}

func (s *Stream) statusLocked(now time.Time) StreamStatus {
	st := StreamStatus{
		Source:          s.source,
		LeaseUntil:      s.leaseUntil,
		MaxFrames:       s.opts.MaxFrames,
		FrameDelay:      s.opts.FrameDelay,
		FlushAfter:      s.opts.FlushAfter,
		MinClipInterval: s.opts.MinClipInterval,
		Received:        s.received,
		Coalesced:       s.coalesced,
		DroppedFrames:   s.dropped,
		ClipsBuilt:      s.clipsBuilt,
		BuildingFrames:  len(s.building),
		ReadyFrames:     len(s.ready),
		CurrentFrames:   len(s.current),
		FirstFrameAt:    s.buildingStart,
		LastFrameAt:     s.lastFrameAt,
		LastClipBuiltAt: s.lastClipAt,
	}

	if len(s.building) > 0 {
		st.NextFlushAt = s.buildingStart.Add(s.opts.FlushAfter)
	}
	if len(s.ready) > 0 && len(s.current) > 0 && !s.readyForced {
		st.NextClipAt = s.lastClipAt.Add(s.opts.MinClipInterval)
	}
	if !s.leaseUntil.After(now) && s.source != "" {
		// The source remains useful history, but an expired lease should not be
		// presented as active ownership.
		st.LeaseUntil = time.Time{}
	}

	return st
}
