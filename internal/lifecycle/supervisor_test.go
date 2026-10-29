package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otaqq/kagemusha/internal/render"
)

type fakeEngine struct {
	mu        sync.Mutex
	launchErr error
	healthy   bool
	closed    bool
	renders   int
	block     chan struct{}
}

func (f *fakeEngine) Launch(context.Context) error { return f.launchErr }
func (f *fakeEngine) Render(ctx context.Context, req render.Request) (render.Result, error) {
	f.mu.Lock()
	f.renders++
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	return render.Result{HTML: "ok", StatusCode: 200, Trigger: render.TriggerReady}, nil
}
func (f *fakeEngine) Healthy(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy
}
func (f *fakeEngine) Close(context.Context) error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) setHealthy(v bool) { f.mu.Lock(); f.healthy = v; f.mu.Unlock() }

func factoryOf(engines ...*fakeEngine) (Factory, *int32) {
	var i int32
	return func() render.Engine {
		n := atomic.AddInt32(&i, 1) - 1
		if int(n) >= len(engines) {
			n = int32(len(engines) - 1)
		}
		return engines[n]
	}, &i
}

func TestSupervisor_StartFailsWhenLaunchFails(t *testing.T) {
	f, _ := factoryOf(&fakeEngine{launchErr: errors.New("no chrome")})
	s := NewSupervisor(f, Options{})
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if _, err := s.Render(context.Background(), render.Request{}); err == nil {
		t.Fatal("render without engine must fail")
	}
	if s.Healthy(context.Background()) {
		t.Fatal("no engine → unhealthy")
	}
}

func TestSupervisor_WatchdogReplacesUnhealthyEngine(t *testing.T) {
	e1 := &fakeEngine{healthy: true}
	e2 := &fakeEngine{healthy: true}
	f, created := factoryOf(e1, e2)
	s := NewSupervisor(f, Options{CheckInterval: 10 * time.Millisecond, WatchdogTimeout: 30 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e1.setHealthy(false)
	deadline := time.Now().Add(2 * time.Second)
	for s.Restarts() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Restarts() != 1 || atomic.LoadInt32(created) != 2 {
		t.Fatalf("restarts=%d created=%d", s.Restarts(), *created)
	}
	e1.mu.Lock()
	closed := e1.closed
	e1.mu.Unlock()
	if !closed {
		t.Fatal("old engine must be closed")
	}
	res, err := s.Render(ctx, render.Request{})
	if err != nil || res.HTML != "ok" || e2.renders != 1 {
		t.Fatalf("render on new engine: %v %+v renders=%d", err, res, e2.renders)
	}
}

func TestSupervisor_PlannedRestartAndRequestRestart(t *testing.T) {
	engines := []*fakeEngine{{healthy: true}, {healthy: true}, {healthy: true}}
	f, _ := factoryOf(engines...)
	s := NewSupervisor(f, Options{CheckInterval: time.Hour, RestartPeriod: 30 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if s.Restarts() < 1 {
		t.Fatal("planned restart did not happen")
	}
	before := s.Restarts()
	s.RequestRestart()
	s.RequestRestart() // a duplicate request collapses into one
	time.Sleep(20 * time.Millisecond)
	if s.Restarts() < before+1 {
		t.Fatal("requested restart did not happen")
	}
}

func TestSupervisor_KeepsOldEngineWhenRestartFails(t *testing.T) {
	e1 := &fakeEngine{healthy: true}
	bad := &fakeEngine{launchErr: errors.New("boom")}
	f, _ := factoryOf(e1, bad)
	s := NewSupervisor(f, Options{CheckInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s.doRestart(ctx)
	if s.Restarts() != 0 || !s.Healthy(ctx) {
		t.Fatal("old engine must remain")
	}
}

func TestSupervisor_ExitPolicyCallsOnExitOnce(t *testing.T) {
	e := &fakeEngine{healthy: true}
	f, _ := factoryOf(e)
	var calls int32
	var reason ExitReason
	s := NewSupervisor(f, Options{CheckInterval: time.Hour, Policy: newPolicy(2, 0, 0), OnExit: func(r ExitReason) { atomic.AddInt32(&calls, 1); reason = r }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Render(ctx, render.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&calls) != 1 || reason != ExitMaxRenders {
		t.Fatalf("calls=%d reason=%q", calls, reason)
	}
}

func TestSupervisor_TimeBasedExitFromCheckLoop(t *testing.T) {
	e := &fakeEngine{healthy: true}
	f, _ := factoryOf(e)
	p := newPolicy(0, time.Millisecond, 0)
	done := make(chan ExitReason, 1)
	s := NewSupervisor(f, Options{CheckInterval: 5 * time.Millisecond, WatchdogTimeout: time.Hour, Policy: p, OnExit: func(r ExitReason) { done <- r }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r != ExitMaxAge {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("OnExit not called")
	}
}

func TestSupervisor_ClosesEngineWhenCtxEnds(t *testing.T) {
	e := &fakeEngine{healthy: true}
	f, _ := factoryOf(e)
	s := NewSupervisor(f, Options{CheckInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		c := e.closed
		e.mu.Unlock()
		if c {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("engine not closed on ctx cancel")
}
