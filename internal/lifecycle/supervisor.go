package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/otaqq/kagemusha/internal/render"
)

// Factory creates a new, not yet launched, engine.
type Factory func() render.Engine

// Options configures the supervisor loop.
type Options struct {
	LaunchTimeout   time.Duration // limit for launching the engine
	WatchdogTimeout time.Duration // force a restart after being unhealthy this long
	RestartPeriod   time.Duration // planned restart period (0 disables)
	CheckInterval   time.Duration // health check interval
	Policy          *Policy       // voluntary worker exit (nil disables)
	OnExit          func(ExitReason)
	Log             *slog.Logger
}

// Supervisor owns the engine: it respawns it when it dies, restarts it on schedule and runs the watchdog.
// Render delegates to the current engine and, during a swap, waits until the new one is ready.
type Supervisor struct {
	factory Factory
	opt     Options

	mu      sync.RWMutex
	eng     render.Engine
	exited  sync.Once
	restart chan struct{}
	metrics struct {
		sync.Mutex
		restarts int
	}
}

// NewSupervisor creates a Supervisor.
func NewSupervisor(f Factory, opt Options) *Supervisor {
	if opt.LaunchTimeout <= 0 {
		opt.LaunchTimeout = 30 * time.Second
	}
	if opt.CheckInterval <= 0 {
		opt.CheckInterval = time.Second
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Supervisor{factory: f, opt: opt, restart: make(chan struct{}, 1)}
}

// Start launches the first engine and runs the supervisor loop until ctx ends.
func (s *Supervisor) Start(ctx context.Context) error {
	eng, err := s.launch(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.eng = eng
	s.mu.Unlock()
	go s.loop(ctx)
	return nil
}

func (s *Supervisor) launch(ctx context.Context) (render.Engine, error) {
	eng := s.factory()
	lctx, cancel := context.WithTimeout(ctx, s.opt.LaunchTimeout)
	defer cancel()
	if err := eng.Launch(lctx); err != nil {
		return nil, fmt.Errorf("lifecycle: launch: %w", err)
	}
	return eng, nil
}

func (s *Supervisor) loop(ctx context.Context) {
	check := time.NewTicker(s.opt.CheckInterval)
	defer check.Stop()
	var planned <-chan time.Time
	if s.opt.RestartPeriod > 0 {
		t := time.NewTicker(s.opt.RestartPeriod)
		defer t.Stop()
		planned = t.C
	}
	var unhealthySince time.Time
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if s.eng != nil {
				_ = s.eng.Close(context.Background())
			}
			s.mu.Unlock()
			return
		case <-planned:
			s.opt.Log.Info("planned engine restart")
			s.doRestart(ctx)
		case <-s.restart:
			s.doRestart(ctx)
		case <-check.C:
			if s.opt.Policy != nil {
				if r := s.opt.Policy.Check(); r != ExitNone {
					s.exit(r)
				}
			}
			if s.Healthy(ctx) {
				unhealthySince = time.Time{}
				continue
			}
			if unhealthySince.IsZero() {
				unhealthySince = time.Now()
			}
			if time.Since(unhealthySince) >= s.opt.WatchdogTimeout {
				s.opt.Log.Warn("watchdog: engine unhealthy, restarting", "unhealthy_for", time.Since(unhealthySince))
				unhealthySince = time.Time{}
				s.doRestart(ctx)
			}
		}
	}
}

// doRestart launches the new engine first, swaps it in, then closes the old one (minimizing waits during the swap).
// The swap takes the write lock, so in-flight renders (holding read locks) finish first.
func (s *Supervisor) doRestart(ctx context.Context) {
	neweng, err := s.launch(ctx)
	if err != nil {
		s.opt.Log.Error("engine relaunch failed", "err", err)
		return
	}
	s.mu.Lock()
	old := s.eng
	s.eng = neweng
	s.mu.Unlock()
	s.metrics.Lock()
	s.metrics.restarts++
	s.metrics.Unlock()
	if old != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = old.Close(cctx)
	}
}

// RequestRestart asks for a restart from outside (e.g. after repeated render failures).
func (s *Supervisor) RequestRestart() {
	select {
	case s.restart <- struct{}{}:
	default:
	}
}

// Render renders with the current engine and records the render in the exit policy.
func (s *Supervisor) Render(ctx context.Context, req render.Request) (render.Result, error) {
	s.mu.RLock()
	eng := s.eng
	s.mu.RUnlock()
	if eng == nil {
		return render.Result{}, errors.New("lifecycle: no engine")
	}
	res, err := eng.Render(ctx, req)
	if s.opt.Policy != nil {
		if r := s.opt.Policy.Record(); r != ExitNone {
			s.exit(r)
		}
	}
	return res, err
}

// Healthy reports the real state of the current engine.
func (s *Supervisor) Healthy(ctx context.Context) bool {
	s.mu.RLock()
	eng := s.eng
	s.mu.RUnlock()
	return eng != nil && eng.Healthy(ctx)
}

// Restarts returns the number of restarts (for metrics).
func (s *Supervisor) Restarts() int {
	s.metrics.Lock()
	defer s.metrics.Unlock()
	return s.metrics.restarts
}

func (s *Supervisor) exit(r ExitReason) {
	s.exited.Do(func() {
		s.opt.Log.Info("worker exit requested", "reason", r)
		if s.opt.OnExit != nil {
			s.opt.OnExit(r)
		}
	})
}
