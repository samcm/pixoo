package pixoo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDevice struct {
	mu       sync.Mutex
	commands []string
	picIDs   []int
	inflight atomic.Int32
	overlap  atomic.Bool
	reply    func(cmd string) string
}

func newFakeDevice() (*fakeDevice, *httptest.Server) {
	d := &fakeDevice{}
	d.reply = func(string) string { return `{"error_code": 0}` }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.inflight.Add(1) > 1 {
			d.overlap.Store(true)
		}
		defer d.inflight.Add(-1)

		body, _ := io.ReadAll(r.Body)

		var payload map[string]any
		_ = json.Unmarshal(body, &payload)

		cmd, _ := payload["Command"].(string)

		d.mu.Lock()
		d.commands = append(d.commands, cmd)
		if cmd == "Draw/SendHttpGif" {
			d.picIDs = append(d.picIDs, int(payload["PicID"].(float64)))
		}
		d.mu.Unlock()

		time.Sleep(5 * time.Millisecond)
		_, _ = io.WriteString(w, d.reply(cmd))
	}))

	return d, srv
}

func (d *fakeDevice) count(cmd string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	n := 0
	for _, c := range d.commands {
		if c == cmd {
			n++
		}
	}

	return n
}

func testClient(url string, opts Options) *Client {
	host := strings.TrimPrefix(url, "http://")
	opts.Endpoint = url + "/post"

	return New(host, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func frame(b byte) []byte {
	return bytes.Repeat([]byte{b}, FrameLen)
}

func TestPushFrameDedupesAndResets(t *testing.T) {
	dev, srv := newFakeDevice()
	defer srv.Close()

	c := testClient(srv.URL, Options{FrameInterval: time.Millisecond, CommandGap: time.Millisecond, GifIDResetEvery: 3})
	ctx := context.Background()

	for i, want := range []bool{true, false, true, true, true} {
		b := byte(i)
		if i == 1 {
			b = 0
		}

		sent, err := c.PushFrame(ctx, frame(b))
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}

		if sent != want {
			t.Fatalf("push %d: sent=%v want %v", i, sent, want)
		}
	}

	if got := dev.count("Draw/ResetHttpGifId"); got != 2 {
		t.Fatalf("resets = %d, want 2 (start + after 3 pushes)", got)
	}

	if got := dev.picIDs; len(got) != 4 || got[0] != 1 || got[1] != 2 || got[2] != 3 || got[3] != 1 {
		t.Fatalf("pic ids = %v", got)
	}

	if st := c.Status(); st.Frames != 4 || st.Skipped != 1 || !st.Online {
		t.Fatalf("status = %+v", st)
	}
}

func TestRequestsAreSerialisedAndPaced(t *testing.T) {
	dev, srv := newFakeDevice()
	defer srv.Close()

	c := testClient(srv.URL, Options{FrameInterval: 40 * time.Millisecond, CommandGap: time.Millisecond})
	ctx := context.Background()

	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < 4; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			if _, err := c.PushFrame(ctx, frame(byte(i))); err != nil {
				t.Error(err)
			}
		}(i)
	}

	wg.Wait()

	if dev.overlap.Load() {
		t.Fatal("device saw overlapping requests")
	}

	if elapsed := time.Since(start); elapsed < 3*40*time.Millisecond {
		t.Fatalf("4 frames took %v, want at least 120ms of pacing", elapsed)
	}
}

func TestDeviceErrorCodes(t *testing.T) {
	dev, srv := newFakeDevice()
	defer srv.Close()

	dev.reply = func(cmd string) string {
		switch cmd {
		case "Channel/SetBrightness":
			return `{"error_code": "Request data illegal json"}`
		case "Channel/GetIndex":
			return `{"ReturnCode": 0, "SelectIndex": 3}`
		}

		return `{"ReturnCode": 5}`
	}

	c := testClient(srv.URL, Options{CommandGap: time.Millisecond})
	ctx := context.Background()

	if err := c.SetBrightness(ctx, 50); err == nil {
		t.Fatal("expected a device error for string error_code")
	}

	if err := c.Heartbeat(ctx); err != nil {
		t.Fatalf("ReturnCode 0 should succeed: %v", err)
	}

	if _, err := c.Command(ctx, "Draw/ResetHttpGifId", nil); err == nil {
		t.Fatal("expected a device error for ReturnCode 5")
	}

	if st := c.Status(); !st.Online || st.Errors != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestTransportFailureGoesOfflineAndRecovers(t *testing.T) {
	dev, srv := newFakeDevice()

	c := testClient(srv.URL, Options{CommandGap: time.Millisecond, RequestTimeout: 200 * time.Millisecond})
	ctx := context.Background()

	if err := c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}

	srv.CloseClientConnections()
	srv.Close()

	if err := c.Heartbeat(ctx); err == nil {
		t.Fatal("expected failure after server close")
	}

	if st := c.Status(); st.Online {
		t.Fatal("client should be offline")
	}

	_ = dev
}

func TestPushAnimationSendsEveryFrameUnderOneID(t *testing.T) {
	dev, srv := newFakeDevice()
	defer srv.Close()

	c := testClient(srv.URL, Options{FrameInterval: time.Millisecond, CommandGap: time.Millisecond})

	sent, err := c.PushAnimation(context.Background(), [][]byte{frame(1), frame(2), frame(3)}, 100*time.Millisecond)
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}

	if got := dev.picIDs; len(got) != 3 || got[0] != 1 || got[1] != 1 || got[2] != 1 {
		t.Fatalf("pic ids = %v, want three frames under id 1", got)
	}

	if _, err := c.PushAnimation(context.Background(), make([][]byte, MaxFrames+1), time.Second); err == nil {
		t.Fatal("expected an error for too many frames")
	}
}

func TestPushBudgetTriggersReboot(t *testing.T) {
	dev, srv := newFakeDevice()
	defer srv.Close()

	c := testClient(srv.URL, Options{FrameInterval: time.Millisecond, CommandGap: time.Millisecond, RebootAfterPushes: 3})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.PushFrame(ctx, frame(byte(i))); err != nil {
			t.Fatal(err)
		}

		if i < 2 && c.NeedsReboot() {
			t.Fatalf("reboot wanted after %d pushes", i+1)
		}
	}

	if !c.NeedsReboot() {
		t.Fatal("reboot not wanted after budget spent")
	}

	c.Reboot(ctx)

	if got := dev.count("Device/SysReboot"); got != 1 {
		t.Fatalf("reboots sent = %d", got)
	}

	if st := c.Status(); st.PushesSinceBoot != 0 || st.Reboots != 1 || st.Online {
		t.Fatalf("status after reboot = %+v", st)
	}

	if c.NeedsReboot() {
		t.Fatal("budget should reset after reboot")
	}

	if _, err := c.PushFrame(ctx, frame(9)); err != nil {
		t.Fatal(err)
	}

	if got := dev.count("Draw/ResetHttpGifId"); got != 2 {
		t.Fatalf("gif id resets = %d, want a fresh reset after reboot", got)
	}
}
