// Package server provides the HTTP layer and the response contract.
// Response headers are built explicitly by kagemusha; headers of the upstream page are never inherited.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/otaqq/kagemusha/internal/cache"
	"github.com/otaqq/kagemusha/internal/flow"
	"github.com/otaqq/kagemusha/internal/metrics"
	"github.com/otaqq/kagemusha/internal/netguard"
	"github.com/otaqq/kagemusha/internal/render"
)

// Renderer runs renders (a render.Engine wrapped with flow control and lifecycle management).
type Renderer interface {
	Render(ctx context.Context, req render.Request) (render.Result, error)
	Healthy(ctx context.Context) bool
}

// StallDetector reports a flow-control stall (implemented by flow.Gate).
type StallDetector interface {
	Stalled(d time.Duration) bool
}

// TargetGuard validates the URL to render (SSRF protection). Implemented by netguard.Guard.
type TargetGuard interface {
	CheckTarget(ctx context.Context, u *url.URL) error
}

// Options configures the HTTP layer.
type Options struct {
	CacheControl string
	DenyPaths    []string
	QueueTimeout time.Duration
	Request      render.Request // defaults other than the URL and Headers (ReadyFlag/IdleQuiet/PageTimeout/block lists/guard)
	// Guard rejects targets outside the allowlist or on non-public addresses (nil disables the check).
	Guard TargetGuard
	// ForwardHeaders are incoming request headers passed on to the browser (e.g. Authorization for a Basic-auth staging site).
	ForwardHeaders []string
	// ResponseHeaders are added to every render response (e.g. X-Robots-Tag: noindex).
	ResponseHeaders http.Header
	// StripQueryParams are removed from the target URL before rendering and single-flight (a trailing * matches any suffix).
	StripQueryParams []string
	// Cache holds recent 200 renders under the single-flight key (nil disables it).
	Cache *cache.LRU[render.Result]
	// Metrics enables GET /metrics and request/render accounting (nil disables both).
	Metrics *metrics.Metrics
	// StallTimeout makes /healthz answer 503 when every slot has been busy this long without progress (0 disables).
	StallTimeout time.Duration
	// Stall overrides the stall detector (defaults to the gate; replaced in tests).
	Stall StallDetector
}

// New builds the http.Handler.
func New(r Renderer, gate *flow.Gate, opt Options, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	if opt.Stall == nil {
		opt.Stall = gate
	}
	s := &server{r: r, gate: gate, opt: opt, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /render", s.render)
	if opt.Metrics != nil {
		mux.HandleFunc("GET /metrics", s.metrics)
	}
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
	if s.opt.Stall.Stalled(s.opt.StallTimeout) {
		s.log.Warn("healthz: flow control stalled", "stall_timeout", s.opt.StallTimeout)
		http.Error(w, "stalled", http.StatusServiceUnavailable)
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
	for k, vs := range s.opt.ResponseHeaders {
		w.Header()[k] = vs
	}
	noStore(w)
	e := &exchange{start: time.Now(), url: target, ua: req.UserAgent()}
	defer s.finish(e)

	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		e.fail(w, "invalid_url", http.StatusBadRequest, "invalid url", nil)
		return
	}
	stripQuery(u, s.opt.StripQueryParams)
	e.url = u.String()
	if s.opt.Guard != nil {
		if err := s.opt.Guard.CheckTarget(req.Context(), u); err != nil {
			if errors.Is(err, netguard.ErrNotAllowed) {
				e.fail(w, "target_not_allowed", http.StatusForbidden, "target not allowed", err)
			} else {
				e.fail(w, "target_lookup_failed", http.StatusBadGateway, "target lookup failed", err)
			}
			return
		}
	}
	for _, p := range s.opt.DenyPaths {
		if strings.HasPrefix(u.Path, p) {
			e.fail(w, "denied_path", http.StatusNotFound, "not found", nil)
			return
		}
	}
	rreq := s.opt.Request
	rreq.URL = e.url
	rreq.Headers = s.forwarded(req)

	key := flightKey(rreq)
	if s.opt.Cache != nil {
		if res, age, ok := s.opt.Cache.Get(key); ok {
			w.Header().Set("X-Kagemusha-Cache", "hit")
			w.Header().Set("Age", strconv.Itoa(int(age.Seconds())))
			e.outcome, e.status, e.trigger = "cache_hit", statusOf(res), res.Trigger
			writeResult(w, res, s.opt.CacheControl)
			return
		}
		w.Header().Set("X-Kagemusha-Cache", "miss")
	}

	// Run the render with its own ctx so a primary disconnect does not cancel it
	v, err := s.gate.Do(req.Context(), context.Background(), key, s.opt.QueueTimeout, func(ctx context.Context) (any, error) {
		res, err := s.r.Render(ctx, rreq)
		if err == nil {
			s.opt.Metrics.Render(statusOf(res), string(res.Trigger), res.Duration)
		}
		if err == nil && s.opt.Cache != nil && statusOf(res) == http.StatusOK {
			s.opt.Cache.Set(key, res)
		}
		return res, err
	})
	if err != nil {
		if errors.Is(err, flow.ErrQueueTimeout) {
			e.fail(w, "queue_timeout", http.StatusServiceUnavailable, "busy", nil)
		} else {
			e.fail(w, "render_failed", http.StatusBadGateway, "render failed", err)
		}
		return
	}
	res := v.(render.Result)
	e.outcome, e.status, e.trigger, e.rendered = "rendered", statusOf(res), res.Trigger, res.Duration
	writeResult(w, res, s.opt.CacheControl)
}

// exchange is what one render request ended with; finish logs it as one line and counts it.
type exchange struct {
	start    time.Time
	url, ua  string
	outcome  string
	status   int
	trigger  render.Trigger
	rendered time.Duration // 0 when the request did not wait for a render (cache hit, rejection)
	err      error
}

func (e *exchange) fail(w http.ResponseWriter, outcome string, status int, body string, err error) {
	e.outcome, e.status, e.err = outcome, status, err
	http.Error(w, body, status)
}

// finish writes the request's single log line: error for failed renders, warn for load
// shedding and rejected targets, info otherwise. Outcomes match kagemusha_requests_total.
func (s *server) finish(e *exchange) {
	s.opt.Metrics.Request(e.outcome)
	attrs := []any{"outcome", e.outcome, "status", e.status, "url", e.url, "ua", e.ua,
		"total_ms", time.Since(e.start).Milliseconds()}
	if e.trigger != "" {
		attrs = append(attrs, "trigger", e.trigger)
	}
	if e.rendered > 0 {
		attrs = append(attrs, "render_ms", e.rendered.Milliseconds())
	}
	if e.err != nil {
		attrs = append(attrs, "err", e.err.Error())
	}
	switch e.outcome {
	case "render_failed":
		s.log.Error("request", attrs...)
	case "queue_timeout", "target_not_allowed", "target_lookup_failed":
		s.log.Warn("request", attrs...)
	default:
		s.log.Info("request", attrs...)
	}
}

// stripQuery removes matching parameters from u, keeping the order and encoding of the rest.
func stripQuery(u *url.URL, patterns []string) {
	if len(patterns) == 0 || u.RawQuery == "" {
		return
	}
	var kept []string
	for _, part := range strings.Split(u.RawQuery, "&") {
		name, _, _ := strings.Cut(part, "=")
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}
		if !matchParam(name, patterns) {
			kept = append(kept, part)
		}
	}
	u.RawQuery = strings.Join(kept, "&")
	if u.RawQuery == "" {
		u.ForceQuery = false
	}
}

func matchParam(name string, patterns []string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
				return true
			}
		} else if strings.EqualFold(name, p) {
			return true
		}
	}
	return false
}

// forwarded picks the configured headers from the incoming request.
func (s *server) forwarded(req *http.Request) map[string]string {
	var h map[string]string
	for _, name := range s.opt.ForwardHeaders {
		if v := req.Header.Get(name); v != "" {
			if h == nil {
				h = map[string]string{}
			}
			h[name] = v
		}
	}
	return h
}

// flightKey is the single-flight key. Forwarded headers are part of it, so a request without
// credentials never shares the result of one with credentials.
func flightKey(r render.Request) string {
	if len(r.Headers) == 0 {
		return r.URL
	}
	names := make([]string, 0, len(r.Headers))
	for n := range r.Headers {
		names = append(names, n)
	}
	slices.Sort(names)
	sum := sha256.New()
	for _, n := range names {
		sum.Write([]byte(n + ":" + r.Headers[n] + "\n"))
	}
	return r.URL + " " + hex.EncodeToString(sum.Sum(nil))
}

// writeResult applies the response contract: only 200 gets cacheable headers; anything else is no-store.
// metrics serves the Prometheus text format without taking a flow-control slot.
func (s *server) metrics(w http.ResponseWriter, _ *http.Request) {
	noStore(w)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = s.opt.Metrics.WriteTo(w)
}

func statusOf(res render.Result) int {
	if res.StatusCode == 0 {
		return http.StatusOK
	}
	return res.StatusCode
}

func writeResult(w http.ResponseWriter, res render.Result, cacheControl string) {
	status := statusOf(res)
	if status == http.StatusOK && cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	} else {
		noStore(w)
	}
	if res.Location != "" {
		w.Header().Set("Location", res.Location)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Kagemusha-Trigger", string(res.Trigger))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(res.HTML))
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }
