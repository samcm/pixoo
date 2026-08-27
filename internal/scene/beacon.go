package scene

import (
	"context"
	"fmt"
	"image/color"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/samcm/pixoo/internal/beacon"
	"github.com/samcm/pixoo/internal/render"
)

type beaconScene struct {
	name   string
	client *beacon.Client
	ours   map[uint64]bool
	logger *slog.Logger
	every  time.Duration

	mu         sync.Mutex
	genesis    time.Time
	head       beacon.Head
	headAt     time.Time
	finality   beacon.Finality
	finalityAt time.Time
	syncing    beacon.Syncing
	syncingAt  time.Time
	duties     map[uint64][]beacon.Duty
	lastErr    error
	lastErrAt  time.Time
}

func newBeacon(name string, opts map[string]any, deps Deps) (Scene, error) {
	if deps.Beacon == nil {
		return nil, fmt.Errorf("scene %s: beacon.url is not configured", name)
	}

	ours := make(map[uint64]bool, len(deps.Validators))
	for _, v := range deps.Validators {
		ours[v] = true
	}

	// Every render is a pushed frame and the panel leaks heap per push, so
	// the default is one render per slot rather than a smooth per-second bar.
	refresh := beacon.SlotDuration
	if v, err := time.ParseDuration(optString(opts, "refresh", "")); err == nil && v > 0 {
		refresh = v
	}

	return &beaconScene{
		name:   name,
		client: deps.Beacon,
		ours:   ours,
		logger: deps.Logger.WithGroup("beacon"),
		every:  refresh,
		duties: map[uint64][]beacon.Duty{},
	}, nil
}

func (b *beaconScene) Name() string { return b.name }
func (b *beaconScene) Kind() string { return "beacon" }

func (b *beaconScene) Render(ctx context.Context, now time.Time) (Frame, time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refresh(ctx, now)

	cv := render.New()
	cv.Clear(render.Black)

	if b.genesis.IsZero() {
		cv.TextCentered(20, "BEACON", render.Small, render.Grey, 1)
		cv.TextCentered(32, "NO NODE", render.Tiny, render.Red, 1)

		return Frame{Image: cv.Img}, 5 * time.Second, nil
	}

	slot, frac := beacon.SlotAt(b.genesis, now)
	epoch := slot / beacon.SlotsPerEpoch
	slotInEpoch := slot % beacon.SlotsPerEpoch

	cv.Text(1, 1, "EPOCH", render.Tiny, render.Grey, 1)
	cv.TextRight(63, 1, strconv.FormatUint(epoch, 10), render.Tiny, render.Cyan, 1)

	cv.Text(1, 9, "SLOT", render.Tiny, render.Grey, 1)
	cv.TextRight(63, 8, strconv.FormatUint(slot, 10), render.Small, render.White, 1)

	if b.every <= 4*time.Second {
		cv.Bar(1, 17, 62, 3, frac, render.Green, render.Dim)
	} else {
		cv.HLine(1, 62, 18, render.Dim)
	}

	cv.Bar(1, 22, 62, 2, (float64(slotInEpoch)+frac)/beacon.SlotsPerEpoch, render.Blue, render.Dim)

	headLag := int64(slot) - int64(b.head.Slot)
	headCol := render.Green

	switch {
	case b.headAt.IsZero() || time.Since(b.headAt) > 30*time.Second:
		headCol = render.Grey
	case headLag > 2:
		headCol = render.Red
	case headLag > 0:
		headCol = render.Yellow
	}

	cv.Text(1, 27, "HEAD", render.Tiny, render.Grey, 1)
	cv.Text(21, 27, fmt.Sprintf("-%d", headLag), render.Tiny, headCol, 1)

	finLag := int64(epoch) - int64(b.finality.Finalized)
	finCol := render.Green

	switch {
	case b.finalityAt.IsZero():
		finCol = render.Grey
	case finLag > 3:
		finCol = render.Red
	case finLag > 2:
		finCol = render.Yellow
	}

	cv.Text(36, 27, "FIN", render.Tiny, render.Grey, 1)
	cv.Text(52, 27, fmt.Sprintf("-%d", finLag), render.Tiny, finCol, 1)

	b.drawProposer(cv, slot)

	status, statusCol := "SYNCED", render.Green

	switch {
	case b.syncingAt.IsZero():
		status, statusCol = "NO DATA", render.Grey
	case b.syncing.IsSyncing:
		status, statusCol = fmt.Sprintf("SYNCING -%d", b.syncing.SyncDistance), render.Orange
	case b.syncing.IsOptimistic:
		status, statusCol = "OPTIMISTIC", render.Yellow
	}

	if b.lastErr != nil && time.Since(b.lastErrAt) < 30*time.Second {
		status, statusCol = "NODE ERROR", render.Red
	}

	cv.HLine(1, 62, 56, render.Dim)
	cv.TextCentered(58, status, render.Tiny, statusCol, 1)

	return Frame{Image: cv.Img}, b.nextRender(now), nil
}

// nextRender aligns renders to slot boundaries when refreshing per slot so
// the slot number changes as soon as the slot does.
func (b *beaconScene) nextRender(now time.Time) time.Duration {
	if b.every < beacon.SlotDuration {
		return b.every
	}

	elapsed := now.Sub(b.genesis) % beacon.SlotDuration

	return beacon.SlotDuration - elapsed + 200*time.Millisecond
}

func (b *beaconScene) drawProposer(cv *render.Canvas, slot uint64) {
	current := b.proposer(slot)
	next := b.proposer(slot + 1)

	cv.Text(1, 34, "NOW", render.Tiny, render.Grey, 1)
	b.drawValidator(cv, 17, 34, current)

	cv.Text(1, 41, "NXT", render.Tiny, render.Grey, 1)
	b.drawValidator(cv, 17, 41, next)

	if len(b.ours) == 0 {
		return
	}

	if d, ok := b.nextOurs(slot); ok {
		in := int64(d.Slot) - int64(slot)
		label := fmt.Sprintf("YOU IN %d", in)

		if in <= 0 {
			label = "YOU NOW!"
		}

		cv.FillRect(1, 48, 62, 7, render.Yellow)
		cv.TextCentered(49, label, render.Tiny, render.Black, 1)

		return
	}

	cv.TextCentered(49, "NO DUTY", render.Tiny, render.Dim, 1)
}

func (b *beaconScene) drawValidator(cv *render.Canvas, x, y int, d *beacon.Duty) {
	if d == nil {
		cv.Text(x, y, "?", render.Tiny, render.Dim, 1)

		return
	}

	col := color.RGBA(render.White)
	if b.ours[d.Validator] {
		col = render.Yellow
	}

	cv.Text(x, y, strconv.FormatUint(d.Validator, 10), render.Tiny, col, 1)
}

func (b *beaconScene) proposer(slot uint64) *beacon.Duty {
	for _, d := range b.duties[slot/beacon.SlotsPerEpoch] {
		if d.Slot == slot {
			return &d
		}
	}

	return nil
}

func (b *beaconScene) nextOurs(slot uint64) (beacon.Duty, bool) {
	epoch := slot / beacon.SlotsPerEpoch

	var best *beacon.Duty

	for _, e := range []uint64{epoch, epoch + 1} {
		for _, d := range b.duties[e] {
			if !b.ours[d.Validator] || d.Slot < slot {
				continue
			}

			if best == nil || d.Slot < best.Slot {
				d := d
				best = &d
			}
		}
	}

	if best == nil {
		return beacon.Duty{}, false
	}

	return *best, true
}

func (b *beaconScene) refresh(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if b.genesis.IsZero() {
		g, err := b.client.Genesis(ctx)
		if err != nil {
			b.fail(err)

			return
		}

		b.genesis = g
	}

	slot, _ := beacon.SlotAt(b.genesis, now)
	epoch := slot / beacon.SlotsPerEpoch

	if time.Since(b.headAt) >= 4*time.Second {
		if h, err := b.client.Head(ctx); err != nil {
			b.fail(err)
		} else {
			b.head, b.headAt = h, now
		}
	}

	if time.Since(b.finalityAt) >= 60*time.Second {
		if f, err := b.client.Finality(ctx); err != nil {
			b.fail(err)
		} else {
			b.finality, b.finalityAt = f, now
		}
	}

	if time.Since(b.syncingAt) >= 30*time.Second {
		if s, err := b.client.Syncing(ctx); err != nil {
			b.fail(err)
		} else {
			b.syncing, b.syncingAt = s, now
		}
	}

	for _, e := range []uint64{epoch, epoch + 1} {
		if _, ok := b.duties[e]; ok {
			continue
		}

		d, err := b.client.ProposerDuties(ctx, e)
		if err != nil {
			b.fail(err)

			continue
		}

		b.duties[e] = d
	}

	for e := range b.duties {
		if e+2 < epoch {
			delete(b.duties, e)
		}
	}
}

func (b *beaconScene) fail(err error) {
	if b.lastErr == nil || time.Since(b.lastErrAt) > time.Minute {
		b.logger.Warn("beacon node request failed", slog.String("error", err.Error()))
	}

	b.lastErr = err
	b.lastErrAt = time.Now()
}
