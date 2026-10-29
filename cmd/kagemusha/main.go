// kagemusha is a dynamic rendering server that hands bots a rendered HTML stand-in of an SPA.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/otaqq/kagemusha/internal/cache"
	"github.com/otaqq/kagemusha/internal/config"
	"github.com/otaqq/kagemusha/internal/flow"
	"github.com/otaqq/kagemusha/internal/lifecycle"
	"github.com/otaqq/kagemusha/internal/metrics"
	"github.com/otaqq/kagemusha/internal/netguard"
	"github.com/otaqq/kagemusha/internal/render"
	"github.com/otaqq/kagemusha/internal/render/chrome"
	"github.com/otaqq/kagemusha/internal/server"
)

func main() {
	os.Exit(run())
}

func run() int {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(nil)
	if err != nil {
		log.Error("config", "err", err)
		return 2
	}
	spec, ok := render.Specs[string(cfg.Engine)]
	if !ok {
		log.Error("unknown engine", "engine", cfg.Engine)
		return 2
	}
	path := cfg.EnginePath
	if path == "" {
		path = spec.DefaultPath
	}
	factory := func() render.Engine {
		switch cfg.Engine {
		case config.EngineChrome:
			return chrome.New(path, spec.Args, log)
		default:
			// Lightpanda speaks CDP, so try the same implementation (experimental; capability gaps are handled later)
			return chrome.New(path, spec.Args, log)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	exitCode := 0
	exitCh := make(chan lifecycle.ExitReason, 1)
	policy := lifecycle.NewPolicy(cfg.WorkerMaxRenders, cfg.WorkerMaxAge)
	log.Info("worker limits", "max_renders", policy.MaxRenders, "max_age", policy.MaxAge.Round(time.Second).String())
	sup := lifecycle.NewSupervisor(factory, lifecycle.Options{
		WatchdogTimeout: cfg.WatchdogTimeout,
		RestartPeriod:   cfg.BrowserRestartPeriod,
		Policy:          policy,
		OnExit:          func(r lifecycle.ExitReason) { exitCh <- r },
		Log:             log,
	})
	if err := sup.Start(ctx); err != nil {
		log.Error("engine start failed", "err", err, "engine", cfg.Engine, "path", path)
		return 1
	}

	gate := flow.New(cfg.MaxConcurrent)
	var m *metrics.Metrics
	if cfg.Metrics {
		m = metrics.New()
		m.Gauge("kagemusha_queue_running", "Renders holding a concurrency slot.", func() float64 { r, _ := gate.Stats(); return float64(r) })
		m.Gauge("kagemusha_queue_waiting", "Distinct URLs waiting for a slot.", func() float64 { _, w := gate.Stats(); return float64(w) })
		m.Gauge("kagemusha_max_concurrent", "Configured concurrency slots.", func() float64 { return float64(cfg.MaxConcurrent) })
		m.LabeledCounter("kagemusha_browser_restarts_total", "Successful browser restarts by reason.", "reason", func() map[string]float64 {
			out := map[string]float64{lifecycle.RestartPlanned: 0, lifecycle.RestartWatchdog: 0, lifecycle.RestartRequested: 0}
			for k, v := range sup.RestartsByReason() {
				out[k] = float64(v)
			}
			return out
		})
		m.Counter("kagemusha_browser_relaunch_failures_total", "Restarts that failed to launch a new browser.", func() float64 { return float64(sup.RelaunchFailures()) })
		m.Counter("kagemusha_worker_renders_total", "Renders by this worker (counts toward WORKER_MAX_RENDERS).", func() float64 { return float64(policy.Renders()) })
	}
	guard := netguard.New(cfg.TargetHosts, cfg.AllowPrivateTargets, nil)
	var pageCache *cache.LRU[render.Result]
	if cfg.CacheTTL > 0 {
		pageCache = cache.New[render.Result](cfg.CacheTTL, cfg.CacheMaxEntries, nil)
		m.Gauge("kagemusha_cache_entries", "Entries in the in-process cache.", func() float64 { return float64(pageCache.Len()) })
	}
	h := server.New(sup, gate, server.Options{
		CacheControl:     cfg.CacheControl,
		DenyPaths:        cfg.DenyPaths,
		QueueTimeout:     cfg.QueueTimeout,
		Guard:            guard,
		ForwardHeaders:   cfg.ForwardHeaders,
		ResponseHeaders:  cfg.ResponseHeaders,
		StripQueryParams: cfg.StripQueryParams,
		Cache:            pageCache,
		Metrics:          m,
		Request: render.Request{
			ReadyFlag: cfg.ReadyFlag, IdleQuiet: cfg.IdleQuiet, PageTimeout: cfg.PageTimeout,
			BlockDomains: cfg.BlockDomains, AllowDomains: cfg.AllowDomains, BlockResourceTypes: cfg.BlockResourceTypes,
			Guard: guard, ForwardHosts: cfg.ForwardHeadersHosts, UserAgent: cfg.UserAgent,
			RedirectCrossHost: cfg.RedirectCrossHost, StripScripts: cfg.StripScripts,
		},
	}, log)
	addr := net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.Port))
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("kagemusha listening", "addr", addr, "engine", cfg.Engine, "path", path)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			exitCh <- lifecycle.ExitNone
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("signal received, shutting down")
	case r := <-exitCh:
		exitCode = r.ExitCode()
		log.Info("worker exiting", "reason", r, "exit_code", exitCode)
	}
	// Let in-flight renders finish (graceful). The LB routes new requests to other workers
	sctx, cancel := context.WithTimeout(context.Background(), cfg.PageTimeout+5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	stop()
	return exitCode
}
