// Package server provides the HTTP layer and the response contract.
// Response headers are built explicitly by kagemusha; headers of the upstream page are never inherited.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/otaqq/kagemusha/internal/flow"
	"github.com/otaqq/kagemusha/internal/render"
)

// Renderer runs renders (a render.Engine wrapped with flow control and lifecycle management).
type Renderer interface {
	Render(ctx context.Context, req render.Request) (render.Result, error)
	Healthy(ctx context.Context) bool
}

// Options configures the HTTP layer.
type Options struct {
	CacheControl string
	DenyPaths    []string
	QueueTimeout time.Duration
	Request      render.Request // defaults other than the URL (ReadyFlag/IdleQuiet/PageTimeout/block lists)
}

// New builds the http.Handler.
func New(r Renderer, gate *flow.Gate, opt Options, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	s := &server{r: r, gate: gate, opt: opt, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /render", s.render)
	// ServeMux path cleaning collapses "//" in the prerender-compatible path (/https://host/path),
	// so route it before the mux
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet &&
			(strings.HasPrefix(req.URL.Path, "/http://") || strings.HasPrefix(req.URL.Path, "/https://")) {
			s.renderCompat(w, req)
			return
		}
		mux.ServeHTTP(w, req)
	})
}

type server struct {
	r    Renderer
	gate *flow.Gate
	opt  Options
	log  *slog.Logger
}

// healthz reports the real engine state without taking a flow-control slot.
func (s *server) healthz(w http.ResponseWriter, req *http.Request) {
	noStore(w)
	if !s.r.Healthy(req.Context()) {
		http.Error(w, "engine unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *server) render(w http.ResponseWriter, req *http.Request) {
	s.serve(w, req, req.URL.Query().Get("url"))
}

// renderCompat serves the prerender-compatible path (/https://host/path).
func (s *server) renderCompat(w http.ResponseWriter, req *http.Request) {
	target := strings.TrimPrefix(req.URL.Path, "/")
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	s.serve(w, req, target)
}

func (s *server) serve(w http.ResponseWriter, req *http.Request, target string) {
	noStore(w)
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	for _, p := range s.opt.DenyPaths {
		if strings.HasPrefix(u.Path, p) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	rreq := s.opt.Request
	rreq.URL = u.String()

	waitCtx, cancel := context.WithTimeout(req.Context(), s.opt.QueueTimeout)
	defer cancel()
	start := time.Now()
	// Run the render with its own ctx so a primary disconnect does not cancel it
	v, err := s.gate.Do(waitCtx, context.Background(), rreq.URL, func(ctx context.Context) (any, error) {
		return s.r.Render(ctx, rreq)
	})
	if err != nil {
		if errors.Is(err, flow.ErrQueueTimeout) {
			s.log.Warn("render queue timeout", "url", rreq.URL, "wait_ms", time.Since(start).Milliseconds())
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		s.log.Error("render failed", "url", rreq.URL, "err", err)
		http.Error(w, "render failed", http.StatusBadGateway)
		return
	}
	res := v.(render.Result)
	s.log.Info("render", "url", rreq.URL, "status", res.StatusCode, "trigger", res.Trigger,
		"duration_ms", res.Duration.Milliseconds(), "total_ms", time.Since(start).Milliseconds())
	writeResult(w, res, s.opt.CacheControl)
}

// writeResult applies the response contract: only 200 gets cacheable headers; anything else is no-store.
func writeResult(w http.ResponseWriter, res render.Result, cacheControl string) {
	status := res.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK && cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	} else {
		noStore(w)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Kagemusha-Trigger", string(res.Trigger))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(res.HTML))
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }
