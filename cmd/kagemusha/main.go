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

	"github.com/otaqq/kagemusha/internal/config"
	"github.com/otaqq/kagemusha/internal/flow"
	"github.com/otaqq/kagemusha/internal/lifecycle"
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
	sup := lifecycle.NewSupervisor(factory, lifecycle.Options{
		WatchdogTimeout: cfg.WatchdogTimeout,
		RestartPeriod:   cfg.BrowserRestartPeriod,
		Policy:          lifecycle.NewPolicy(cfg.WorkerMaxRenders, cfg.WorkerMaxAge),
		OnExit:          func(r lifecycle.ExitReason) { exitCh <- r },
		Log:             log,
	})
	if err := sup.Start(ctx); err != nil {
		log.Error("engine start failed", "err", err, "engine", cfg.Engine, "path", path)
		return 1
	}

	guard := netguard.New(cfg.TargetHosts, cfg.AllowPrivateTargets, nil)
	h := server.New(sup, flow.New(cfg.MaxConcurrent), server.Options{
		CacheControl:    cfg.CacheControl,
		DenyPaths:       cfg.DenyPaths,
		QueueTimeout:    cfg.QueueTimeout,
		Guard:           guard,
		ForwardHeaders:  cfg.ForwardHeaders,
		ResponseHeaders: cfg.ResponseHeaders,
		Request: render.Request{
			ReadyFlag: cfg.ReadyFlag, IdleQuiet: cfg.IdleQuiet, PageTimeout: cfg.PageTimeout,
			BlockDomains: cfg.BlockDomains, AllowDomains: cfg.AllowDomains,
			Guard: guard, ForwardHosts: cfg.ForwardHeadersHosts, UserAgent: cfg.UserAgent,
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
