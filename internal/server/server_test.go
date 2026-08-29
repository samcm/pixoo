package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/samcm/pixoo/internal/app"
	"github.com/samcm/pixoo/internal/pixoo"
	"github.com/samcm/pixoo/internal/scene"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := pixoo.New("unused", pixoo.Options{Endpoint: "http://127.0.0.1:1/post"}, logger)
	off, err := scene.New("off", "off", nil, scene.Deps{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	scenes := map[string]scene.Scene{
		"off":    off,
		"stream": scene.NewStream("stream", scene.StreamOptions{SourceLease: time.Minute}),
	}
	a, err := app.New(app.Options{
		Client:   client,
		Scenes:   scenes,
		Rotation: []app.Entry{{Scene: "off", Duration: time.Minute}},
		Logger:   logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	return httptest.NewServer(New(a, logger).Handler())
}

func postStreamFrame(t *testing.T, url, source string) *http.Response {
	t.Helper()

	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if source != "" {
		if err := writer.WriteField("source", source); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "frame.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(pngData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, url+"/api/stream/frame", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	return resp
}

func TestStreamFrameAPIRequiresSourceAndEnforcesLease(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := postStreamFrame(t, srv.URL, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing source status = %d, want 400", resp.StatusCode)
	}

	resp = postStreamFrame(t, srv.URL, "renderer-one")
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first frame status = %d, want 202", resp.StatusCode)
	}

	resp = postStreamFrame(t, srv.URL, "renderer-two")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting source status = %d, want 409", resp.StatusCode)
	}

	var conflict map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&conflict); err != nil {
		t.Fatal(err)
	}
	if conflict["holder"] != "renderer-one" {
		t.Fatalf("conflict response = %#v", conflict)
	}
}

func TestStreamStatusAndFlushAPI(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp := postStreamFrame(t, srv.URL, "renderer")
	resp.Body.Close()

	resp, err := http.Get(srv.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var st scene.StreamStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Source != "renderer" || st.BuildingFrames != 1 {
		t.Fatalf("stream status = %+v", st)
	}

	body := bytes.NewBufferString(`{"source":"renderer","seconds":0}`)
	resp, err = http.Post(srv.URL+"/api/stream/flush", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("flush status = %d, want 202", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.ReadyFrames != 1 || st.BuildingFrames != 0 {
		t.Fatalf("status after flush = %+v", st)
	}
}

func TestBufferedStreamPublishesOneDeviceAnimation(t *testing.T) {
	type command struct {
		Command   string `json:"Command"`
		PicNum    int    `json:"PicNum"`
		PicID     int    `json:"PicID"`
		PicOffset int    `json:"PicOffset"`
	}
	animation := make(chan command, 8)
	device := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var cmd command
		if err := json.NewDecoder(request.Body).Decode(&cmd); err != nil {
			t.Errorf("decode device command: %v", err)
		}
		if cmd.Command == "Draw/SendHttpGif" && cmd.PicNum == 3 {
			animation <- cmd
		}
		_, _ = response.Write([]byte(`{"error_code":0,"SelectIndex":0}`))
	}))
	defer device.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := pixoo.New("fake-panel", pixoo.Options{
		Endpoint:        device.URL,
		FrameInterval:   time.Millisecond,
		CommandGap:      time.Millisecond,
		RequestTimeout:  time.Second,
		RefreshEvery:    time.Hour,
		GifIDResetEvery: 32,
	}, logger)
	off, err := scene.New("off", "off", nil, scene.Deps{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	scenes := map[string]scene.Scene{
		"off": off,
		"stream": scene.NewStream("stream", scene.StreamOptions{
			MaxFrames:   3,
			FrameDelay:  20 * time.Millisecond,
			FlushAfter:  time.Second,
			SourceLease: time.Minute,
		}),
	}
	a, err := app.New(app.Options{
		Client:            client,
		Scenes:            scenes,
		Rotation:          []app.Entry{{Scene: "off", Duration: time.Minute}},
		HeartbeatInterval: time.Hour,
		Logger:            logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	proxy := httptest.NewServer(New(a, logger).Handler())
	defer proxy.Close()
	for range 3 {
		resp := postStreamFrame(t, proxy.URL, "renderer")
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("frame status = %d, want 202", resp.StatusCode)
		}
		time.Sleep(25 * time.Millisecond)
	}

	commands := make([]command, 0, 3)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for len(commands) < 3 {
		select {
		case cmd := <-animation:
			commands = append(commands, cmd)
		case <-timer.C:
			t.Fatalf("timed out after %d animation frames", len(commands))
		}
	}

	for i, cmd := range commands {
		if cmd.PicID != commands[0].PicID || cmd.PicOffset != i {
			t.Fatalf("animation commands = %+v", commands)
		}
	}
}
