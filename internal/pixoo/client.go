// Package pixoo drives a Divoom Pixoo 64 over its LAN HTTP API.
//
// The device runs a stock ESP-IDF http server: one request at a time and a
// hard cap of seven open sockets. Every call goes through a single keep-alive
// connection and a mutex, frames are rate limited and de-duplicated, and any
// transport error drops the connection so the device gets its socket back.
package pixoo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	Size      = 64
	FrameLen  = Size * Size * 3
	MaxFrames = 60
)

type Options struct {
	// Endpoint overrides the default http://<host>/post; newer firmware
	// answers on http://<host>:9000/divoom_api instead.
	Endpoint        string
	FrameInterval   time.Duration
	CommandGap      time.Duration
	RequestTimeout  time.Duration
	RefreshEvery    time.Duration
	GifIDResetEvery int
	// RebootAfterPushes is a legacy safety valve that triggers a
	// Device/SysReboot after this many frames. Zero disables it.
	RebootAfterPushes int
}

func (o *Options) setDefaults() {
	if o.FrameInterval <= 0 {
		o.FrameInterval = time.Second
	}
	if o.CommandGap <= 0 {
		o.CommandGap = 50 * time.Millisecond
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 10 * time.Second
	}
	if o.RefreshEvery <= 0 {
		o.RefreshEvery = 30 * time.Minute
	}
	if o.GifIDResetEvery <= 0 {
		o.GifIDResetEvery = 32
	}
}

type Status struct {
	Online      bool      `json:"online"`
	LastOK      time.Time `json:"last_ok"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at"`
	LatencyMS   int64     `json:"latency_ms"`
	Requests    uint64    `json:"requests"`
	Frames      uint64    `json:"frames"`
	Skipped     uint64    `json:"skipped"`
	Errors      uint64    `json:"errors"`
	PicID       int       `json:"pic_id"`
	// PushesSinceBoot counts frames since the panel last (re)booted as far
	// as this process knows; it resets on Reboot and starts at zero.
	PushesSinceBoot int       `json:"pushes_since_boot"`
	Reboots         uint64    `json:"reboots"`
	LastReboot      time.Time `json:"last_reboot"`
}

type Conf struct {
	Brightness      int  `json:"brightness"`
	LightSwitch     bool `json:"light_switch"`
	ClockID         int  `json:"clock_id"`
	ChannelIndex    int  `json:"channel_index"`
	RotationFlag    int  `json:"rotation_flag"`
	Time24          bool `json:"time_24"`
	TemperatureMode int  `json:"temperature_mode"`
}

type Weather struct {
	Condition string  `json:"condition"`
	Temp      float64 `json:"temp"`
	MinTemp   float64 `json:"min_temp"`
	MaxTemp   float64 `json:"max_temp"`
	Humidity  int     `json:"humidity"`
	WindSpeed float64 `json:"wind_speed"`
}

type DeviceError struct {
	Command string
	Code    any
}

func (e *DeviceError) Error() string {
	return fmt.Sprintf("pixoo: %s returned error_code %v", e.Command, e.Code)
}

type Client struct {
	host      string
	opts      Options
	logger    *slog.Logger
	transport *http.Transport
	http      *http.Client

	mu          sync.Mutex
	lastRequest time.Time
	lastFrame   time.Time
	lastHash    [sha256.Size]byte
	hasLast     bool
	picID       int
	pushes      int
	sinceBoot   int

	stateMu sync.RWMutex
	status  Status
}

func New(host string, opts Options, logger *slog.Logger) *Client {
	opts.setDefaults()

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxConnsPerHost:     1,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     5 * time.Minute,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
	}

	if opts.Endpoint == "" {
		opts.Endpoint = "http://" + host + "/post"
	}

	return &Client{
		host:      host,
		opts:      opts,
		logger:    logger.WithGroup("pixoo"),
		transport: transport,
		http:      &http.Client{Transport: transport},
	}
}

func (c *Client) Host() string { return c.host }

func (c *Client) Status() Status {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return c.status
}

// Invalidate forces the next PushFrame to send even if the frame is unchanged.
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.hasLast = false
}

// Command sends an arbitrary command and returns the decoded response.
func (c *Client) Command(ctx context.Context, command string, args map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.do(ctx, command, args)
}

func (c *Client) Heartbeat(ctx context.Context) error {
	_, err := c.Command(ctx, "Channel/GetIndex", nil)

	return err
}

func (c *Client) SetBrightness(ctx context.Context, value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("pixoo: brightness %d out of range 0-100", value)
	}

	_, err := c.Command(ctx, "Channel/SetBrightness", map[string]any{"Brightness": value})

	return err
}

func (c *Client) SetScreen(ctx context.Context, on bool) error {
	v := 0
	if on {
		v = 1
	}

	_, err := c.Command(ctx, "Channel/OnOffScreen", map[string]any{"OnOff": v})

	return err
}

func (c *Client) SetChannel(ctx context.Context, index int) error {
	_, err := c.Command(ctx, "Channel/SetIndex", map[string]any{"SelectIndex": index})

	return err
}

func (c *Client) GetConf(ctx context.Context) (Conf, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	out, err := c.do(ctx, "Channel/GetAllConf", nil)
	if err != nil {
		return Conf{}, err
	}

	conf := Conf{
		Brightness:      num(out["Brightness"]),
		LightSwitch:     num(out["LightSwitch"]) == 1,
		ClockID:         num(out["CurClockId"]),
		RotationFlag:    num(out["RotationFlag"]),
		Time24:          num(out["Time24Flag"]) == 1,
		TemperatureMode: num(out["TemperatureMode"]),
	}

	idx, err := c.do(ctx, "Channel/GetIndex", nil)
	if err != nil {
		return conf, err
	}

	conf.ChannelIndex = num(idx["SelectIndex"])

	return conf, nil
}

// Weather returns the forecast the device itself pulls from Divoom's cloud.
func (c *Client) Weather(ctx context.Context) (Weather, error) {
	out, err := c.Command(ctx, "Device/GetWeatherInfo", nil)
	if err != nil {
		return Weather{}, err
	}

	w := Weather{
		Temp:      fnum(out["CurTemp"]),
		MinTemp:   fnum(out["MinTemp"]),
		MaxTemp:   fnum(out["MaxTemp"]),
		Humidity:  num(out["Humidity"]),
		WindSpeed: fnum(out["WindSpeed"]),
	}
	if s, ok := out["Weather"].(string); ok {
		w.Condition = s
	}

	return w, nil
}

// SyncTime sets the device clock and timezone from loc.
func (c *Client) SyncTime(ctx context.Context, loc *time.Location) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	_, offset := now.In(loc).Zone()
	hours := offset / 3600

	if _, err := c.do(ctx, "Sys/TimeZone", map[string]any{"TimeZoneValue": fmt.Sprintf("GMT%+d", hours)}); err != nil {
		return err
	}

	_, err := c.do(ctx, "Device/SetUTC", map[string]any{"Utc": now.Unix()})

	return err
}

// PushFrame sends one 64x64 RGB frame. It returns false when the frame was
// identical to the last one pushed and was skipped.
func (c *Client) PushFrame(ctx context.Context, rgb []byte) (bool, error) {
	if len(rgb) != FrameLen {
		return false, fmt.Errorf("pixoo: frame is %d bytes, want %d", len(rgb), FrameLen)
	}

	hash := sha256.Sum256(rgb)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.unchanged(hash) {
		return false, nil
	}

	if err := c.waitFor(ctx, c.lastFrame, c.opts.FrameInterval); err != nil {
		return false, err
	}

	if err := c.ensureGifID(ctx); err != nil {
		return false, err
	}

	if _, err := c.do(ctx, "Draw/SendHttpGif", gifArgs(1, 0, c.picID, 1000, rgb)); err != nil {
		c.hasLast = false

		return false, err
	}

	c.framePushed(hash)

	return true, nil
}

// PushAnimation uploads up to MaxFrames frames that the device then loops on
// its own with the given delay between frames.
func (c *Client) PushAnimation(ctx context.Context, frames [][]byte, delay time.Duration) (bool, error) {
	if len(frames) == 0 || len(frames) > MaxFrames {
		return false, fmt.Errorf("pixoo: animation has %d frames, want 1-%d", len(frames), MaxFrames)
	}

	h := sha256.New()
	for i, f := range frames {
		if len(f) != FrameLen {
			return false, fmt.Errorf("pixoo: frame %d is %d bytes, want %d", i, len(f), FrameLen)
		}

		h.Write(f)
	}

	var hash [sha256.Size]byte
	copy(hash[:], h.Sum(nil))

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.unchanged(hash) {
		return false, nil
	}

	if err := c.waitFor(ctx, c.lastFrame, c.opts.FrameInterval); err != nil {
		return false, err
	}

	if err := c.ensureGifID(ctx); err != nil {
		return false, err
	}

	speed := int(delay / time.Millisecond)
	if speed < 50 {
		speed = 50
	}

	for i, f := range frames {
		if _, err := c.do(ctx, "Draw/SendHttpGif", gifArgs(len(frames), i, c.picID, speed, f)); err != nil {
			c.hasLast = false

			return false, err
		}
	}

	c.framePushed(hash)

	return true, nil
}

func gifArgs(num, offset, id, speed int, rgb []byte) map[string]any {
	return map[string]any{
		"PicNum":    num,
		"PicWidth":  Size,
		"PicOffset": offset,
		"PicID":     id,
		"PicSpeed":  speed,
		"PicData":   base64.StdEncoding.EncodeToString(rgb),
	}
}

func (c *Client) unchanged(hash [sha256.Size]byte) bool {
	if c.hasLast && hash == c.lastHash && time.Since(c.lastFrame) < c.opts.RefreshEvery {
		c.stateMu.Lock()
		c.status.Skipped++
		c.stateMu.Unlock()

		return true
	}

	return false
}

func (c *Client) framePushed(hash [sha256.Size]byte) {
	c.lastHash = hash
	c.hasLast = true
	c.lastFrame = time.Now()
	c.picID++
	c.pushes++
	c.sinceBoot++

	c.stateMu.Lock()
	c.status.Frames++
	c.status.PicID = c.picID
	c.status.PushesSinceBoot = c.sinceBoot
	c.stateMu.Unlock()
}

// NeedsReboot reports whether the push budget for this boot is spent.
func (c *Client) NeedsReboot() bool {
	if c.opts.RebootAfterPushes <= 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.sinceBoot >= c.opts.RebootAfterPushes
}

// Reboot asks the panel to restart. The panel does not answer the request,
// so a transport error here is expected and not counted. It then takes
// ~30 s to come back; callers should heartbeat until it does.
func (c *Client) Reboot(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info("rebooting panel", slog.Int("pushes_since_boot", c.sinceBoot))

	payload, _ := json.Marshal(map[string]any{"Command": "Device/SysReboot"})

	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.opts.Endpoint, bytes.NewReader(payload))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")

		if resp, err := c.http.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	c.transport.CloseIdleConnections()
	c.lastRequest = time.Now()
	c.markRebooted()
}

func (c *Client) markRebooted() {
	c.sinceBoot = 0
	c.picID = 0
	c.pushes = 0
	c.hasLast = false

	c.stateMu.Lock()
	c.status.Online = false
	c.status.PushesSinceBoot = 0
	c.status.Reboots++
	c.status.LastReboot = time.Now()
	c.stateMu.Unlock()
}

func (c *Client) ensureGifID(ctx context.Context) error {
	if c.picID > 0 && c.pushes < c.opts.GifIDResetEvery {
		return nil
	}

	if _, err := c.do(ctx, "Draw/ResetHttpGifId", nil); err != nil {
		return err
	}

	c.picID = 1
	c.pushes = 0

	return nil
}

func (c *Client) waitFor(ctx context.Context, since time.Time, gap time.Duration) error {
	wait := gap - time.Since(since)
	if wait <= 0 {
		return nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) do(ctx context.Context, command string, args map[string]any) (map[string]any, error) {
	payload := make(map[string]any, len(args)+1)
	payload["Command"] = command
	for k, v := range args {
		payload[k] = v
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	if err := c.waitFor(ctx, c.lastRequest, c.opts.CommandGap); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	c.lastRequest = time.Now()

	if err != nil {
		return nil, c.fail(command, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, c.fail(command, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, c.fail(command, fmt.Errorf("http %d: %s", resp.StatusCode, bytes.TrimSpace(raw)))
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, c.fail(command, fmt.Errorf("bad response %q: %w", bytes.TrimSpace(raw), err))
	}

	code, ok := out["error_code"]
	if !ok {
		code, ok = out["ReturnCode"]
	}

	if ok {
		switch v := code.(type) {
		case float64:
			if v != 0 {
				return out, c.deviceError(command, v)
			}
		case string:
			return out, c.deviceError(command, v)
		}
	}

	c.ok(time.Since(start))

	return out, nil
}

// deviceError records a well-formed error reply. The connection stays up: the
// device answered, it just rejected the command.
func (c *Client) deviceError(command string, code any) error {
	err := &DeviceError{Command: command, Code: code}

	c.stateMu.Lock()
	c.status.Errors++
	c.status.LastError = err.Error()
	c.status.LastErrorAt = time.Now()
	c.stateMu.Unlock()

	return err
}

// fail records a transport failure and drops the keep-alive connection so the
// device frees its socket and the next call starts clean.
func (c *Client) fail(command string, err error) error {
	c.transport.CloseIdleConnections()

	c.stateMu.Lock()
	wasOnline := c.status.Online
	c.status.Online = false
	c.status.Errors++
	c.status.LastError = err.Error()
	c.status.LastErrorAt = time.Now()
	c.stateMu.Unlock()

	if wasOnline {
		c.logger.Warn("device offline",
			slog.String("command", command),
			slog.String("error", err.Error()),
			slog.Int("pushes_since_boot", c.sinceBoot))
	}

	return fmt.Errorf("pixoo: %s: %w", command, err)
}

func (c *Client) ok(latency time.Duration) {
	c.stateMu.Lock()
	wasOnline := c.status.Online
	c.status.Online = true
	c.status.LastOK = time.Now()
	c.status.LatencyMS = latency.Milliseconds()
	c.status.Requests++
	c.stateMu.Unlock()

	if !wasOnline {
		c.logger.Info("device online", slog.Int64("latency_ms", latency.Milliseconds()))
	}
}

func num(v any) int {
	f, _ := v.(float64)

	return int(f)
}

func fnum(v any) float64 {
	f, _ := v.(float64)

	return f
}
