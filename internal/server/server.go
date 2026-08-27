// Package server exposes the JSON API and the embedded web UI.
package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/samcm/pixoo/internal/app"
	"github.com/samcm/pixoo/internal/render"
	"github.com/samcm/pixoo/internal/scene"
)

//go:embed ui/index.html
var indexHTML []byte

const maxUpload = 8 << 20

type Server struct {
	app    *app.App
	logger *slog.Logger
	mux    *http.ServeMux
}

func New(a *app.App, logger *slog.Logger) *Server {
	s := &Server{app: a, logger: logger.WithGroup("http"), mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /api/status", s.status)
	s.mux.HandleFunc("GET /api/scenes", s.scenes)
	s.mux.HandleFunc("GET /api/preview.png", s.preview)
	s.mux.HandleFunc("POST /api/show", s.show)
	s.mux.HandleFunc("POST /api/resume", s.resume)
	s.mux.HandleFunc("POST /api/brightness", s.brightness)
	s.mux.HandleFunc("POST /api/screen", s.screen)
	s.mux.HandleFunc("POST /api/text", s.text)
	s.mux.HandleFunc("POST /api/image", s.image)
	s.mux.HandleFunc("POST /api/command", s.command)

	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	st := s.app.Status()
	if !st.Device.Online {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "last_error": st.Device.LastError})

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) scenes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"scenes": s.app.Scenes(), "kinds": scene.Kinds()})
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	scale, _ := strconv.Atoi(r.URL.Query().Get("scale"))
	if scale < 1 || scale > 16 {
		scale = 6
	}

	png, err := render.EncodePNG(s.app.Preview(), scale)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)

		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) show(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scene   string  `json:"scene"`
		Seconds float64 `json:"seconds"`
	}

	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	if err := s.app.Show(req.Scene, seconds(req.Seconds)); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) resume(w http.ResponseWriter, _ *http.Request) {
	s.app.Resume()
	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) brightness(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value int `json:"value"`
	}

	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	if err := s.app.SetBrightness(r.Context(), req.Value); err != nil {
		writeError(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}

	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	if err := s.app.SetScreen(req.On); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) text(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text       string  `json:"text"`
		Color      string  `json:"color"`
		Background string  `json:"background"`
		Font       string  `json:"font"`
		Scroll     bool    `json:"scroll"`
		Seconds    float64 `json:"seconds"`
	}

	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	if req.Text == "" {
		writeError(w, http.StatusBadRequest, errors.New("text is required"))

		return
	}

	opts := scene.TextOptions{Text: req.Text, Color: render.White, Background: render.Black, Font: req.Font, Scroll: req.Scroll}

	if req.Color != "" {
		c, err := scene.ParseColor(req.Color)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)

			return
		}

		opts.Color = c
	}

	if req.Background != "" {
		c, err := scene.ParseColor(req.Background)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)

			return
		}

		opts.Background = c
	}

	if err := s.app.SetText(opts, seconds(req.Seconds)); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)

	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("file field is required"))

		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	secs, _ := strconv.ParseFloat(r.FormValue("seconds"), 64)

	if err := s.app.SetImage(data, header.Filename, seconds(secs)); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusOK, s.app.Status())
}

func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string         `json:"command"`
		Args    map[string]any `json:"args"`
	}

	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	if req.Command == "" {
		writeError(w, http.StatusBadRequest, errors.New("command is required"))

		return
	}

	out, err := s.app.Command(r.Context(), req.Command, req.Args)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "response": out})

		return
	}

	writeJSON(w, http.StatusOK, out)
}

func seconds(v float64) time.Duration {
	if v <= 0 {
		return 0
	}

	return time.Duration(v * float64(time.Second))
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()

	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
