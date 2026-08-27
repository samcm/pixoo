// Package beacon is a minimal Ethereum beacon-node REST client for the
// handful of endpoints the display needs.
package beacon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	SlotDuration  = 12 * time.Second
	SlotsPerEpoch = 32
)

type Client struct {
	base string
	http *http.Client

	mu      sync.Mutex
	genesis time.Time
}

func New(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) URL() string { return c.base }

type Head struct {
	Slot uint64
	Root string
}

type Finality struct {
	Justified uint64
	Finalized uint64
}

type Syncing struct {
	HeadSlot     uint64
	SyncDistance uint64
	IsSyncing    bool
	IsOptimistic bool
}

type Duty struct {
	Slot      uint64
	Validator uint64
}

// Genesis returns the chain genesis time, fetched once and cached.
func (c *Client) Genesis(ctx context.Context) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.genesis.IsZero() {
		return c.genesis, nil
	}

	var out struct {
		Data struct {
			GenesisTime string `json:"genesis_time"`
		} `json:"data"`
	}

	if err := c.get(ctx, "/eth/v1/beacon/genesis", &out); err != nil {
		return time.Time{}, err
	}

	secs, err := strconv.ParseInt(out.Data.GenesisTime, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("beacon: bad genesis time %q", out.Data.GenesisTime)
	}

	c.genesis = time.Unix(secs, 0)

	return c.genesis, nil
}

func (c *Client) Head(ctx context.Context) (Head, error) {
	var out struct {
		Data struct {
			Root   string `json:"root"`
			Header struct {
				Message struct {
					Slot string `json:"slot"`
				} `json:"message"`
			} `json:"header"`
		} `json:"data"`
	}

	if err := c.get(ctx, "/eth/v1/beacon/headers/head", &out); err != nil {
		return Head{}, err
	}

	slot, err := strconv.ParseUint(out.Data.Header.Message.Slot, 10, 64)
	if err != nil {
		return Head{}, fmt.Errorf("beacon: bad head slot %q", out.Data.Header.Message.Slot)
	}

	return Head{Slot: slot, Root: out.Data.Root}, nil
}

func (c *Client) Finality(ctx context.Context) (Finality, error) {
	var out struct {
		Data struct {
			CurrentJustified struct {
				Epoch string `json:"epoch"`
			} `json:"current_justified"`
			Finalized struct {
				Epoch string `json:"epoch"`
			} `json:"finalized"`
		} `json:"data"`
	}

	if err := c.get(ctx, "/eth/v1/beacon/states/head/finality_checkpoints", &out); err != nil {
		return Finality{}, err
	}

	j, _ := strconv.ParseUint(out.Data.CurrentJustified.Epoch, 10, 64)
	f, _ := strconv.ParseUint(out.Data.Finalized.Epoch, 10, 64)

	return Finality{Justified: j, Finalized: f}, nil
}

func (c *Client) Syncing(ctx context.Context) (Syncing, error) {
	var out struct {
		Data struct {
			HeadSlot     string `json:"head_slot"`
			SyncDistance string `json:"sync_distance"`
			IsSyncing    bool   `json:"is_syncing"`
			IsOptimistic bool   `json:"is_optimistic"`
		} `json:"data"`
	}

	if err := c.get(ctx, "/eth/v1/node/syncing", &out); err != nil {
		return Syncing{}, err
	}

	h, _ := strconv.ParseUint(out.Data.HeadSlot, 10, 64)
	d, _ := strconv.ParseUint(out.Data.SyncDistance, 10, 64)

	return Syncing{HeadSlot: h, SyncDistance: d, IsSyncing: out.Data.IsSyncing, IsOptimistic: out.Data.IsOptimistic}, nil
}

// ProposerDuties returns the proposer for every slot of epoch.
func (c *Client) ProposerDuties(ctx context.Context, epoch uint64) ([]Duty, error) {
	var out struct {
		Data []struct {
			Slot           string `json:"slot"`
			ValidatorIndex string `json:"validator_index"`
		} `json:"data"`
	}

	if err := c.get(ctx, "/eth/v1/validator/duties/proposer/"+strconv.FormatUint(epoch, 10), &out); err != nil {
		return nil, err
	}

	duties := make([]Duty, 0, len(out.Data))

	for _, d := range out.Data {
		slot, _ := strconv.ParseUint(d.Slot, 10, 64)
		v, _ := strconv.ParseUint(d.ValidatorIndex, 10, 64)
		duties = append(duties, Duty{Slot: slot, Validator: v})
	}

	return duties, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	u, err := url.JoinPath(c.base, path)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("beacon: %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("beacon: %s: http %d", path, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("beacon: %s: %w", path, err)
	}

	return nil
}

// SlotAt returns the slot in progress at t and how far through it t is.
func SlotAt(genesis, t time.Time) (uint64, float64) {
	if t.Before(genesis) {
		return 0, 0
	}

	elapsed := t.Sub(genesis)
	slot := uint64(elapsed / SlotDuration)
	frac := float64(elapsed%SlotDuration) / float64(SlotDuration)

	return slot, frac
}
