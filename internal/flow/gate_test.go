package flow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_SameKeySharesOneRun(t *testing.T) {
	g := New(4)
	var runs int32
	release := make(chan struct{})
	fn := func(ctx context.Context) (any, error) {
		atomic.AddInt32(&runs, 1)
		<-release
		return "html", nil
	}
	var wg sync.WaitGroup
	results := make([]any, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = g.Do(context.Background(), context.Background(), "u1", 0, fn)
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	if got := g.InFlight(); got != 1 {
		t.Fatalf("InFlight=%d want 1", got)
	}
	close(release)
	wg.Wait()
	if atomic.LoadInt32(&runs) != 1 {
		t.Fatalf("runs=%d want 1", runs)
	}
	for _, r := range results {
		if r != "html" {
			t.Fatalf("result=%v", r)
		}
	}
	if g.InFlight() != 0 {
		t.Fatal("calls not cleaned up")
	}
}

func TestDo_OverCapacityWaitsFIFO(t *testing.T) {
	g := New(1)
	release := make(chan struct{})
	slow := func(ctx context.Context) (any, error) { <-release; return 1, nil }
	go g.Do(context.Background(), context.Background(), "a", 0, slow) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := g.Do(ctx, context.Background(), "b", 0, func(ctx context.Context) (any, error) { return 2, nil })
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("want ErrQueueTimeout, got %v", err)
	}
	close(release)
}

func TestDo_FollowerDeadlineAffectsOnlyFollower(t *testing.T) {
	g := New(2)
	release := make(chan struct{})
	fn := func(ctx context.Context) (any, error) { <-release; return "ok", nil }
	primaryDone := make(chan any)
	go func() { v, _ := g.Do(context.Background(), context.Background(), "k", 0, fn); primaryDone <- v }()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Do(ctx, context.Background(), "k", 0, fn); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("follower: want timeout, got %v", err)
	}
	close(release)
	if v := <-primaryDone; v != "ok" {
		t.Fatalf("primary=%v", v)
	}
}

func TestDo_PanicBecomesErrorWithoutAffectingOthers(t *testing.T) {
	g := New(1)
	_, err := g.Do(context.Background(), context.Background(), "p", 0, func(ctx context.Context) (any, error) { panic("boom") })
	if err == nil {
		t.Fatal("want error")
	}
	v, err := g.Do(context.Background(), context.Background(), "q", 0, func(ctx context.Context) (any, error) { return "fine", nil })
	if err != nil || v != "fine" {
		t.Fatalf("gate unusable after panic: %v %v", v, err)
	}
}

func TestDo_RunsWithRunCtxAndSurvivesPrimaryCancel(t *testing.T) {
	g := New(1)
	got := make(chan string, 1)
	fn := func(ctx context.Context) (any, error) {
		time.Sleep(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			got <- "cancelled"
		default:
			got <- "completed"
		}
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go g.Do(ctx, context.Background(), "r", 0, fn) //nolint:errcheck
	time.Sleep(5 * time.Millisecond)
	cancel()
	if v := <-got; v != "completed" {
		t.Fatalf("want completed, got %s", v)
	}
}

func TestNew_ZeroMaxBecomesOne(t *testing.T) {
	if cap(New(0).slots) != 1 {
		t.Fatal("want 1 slot")
	}
}

// A queued primary that gives up must not take its followers down with it.
func TestDo_QueuedPrimaryCancelDoesNotFailFollowers(t *testing.T) {
	g := New(1)
	release := make(chan struct{})
	go g.Do(context.Background(), context.Background(), "busy", 0, func(ctx context.Context) (any, error) { <-release; return nil, nil }) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)

	var runs int32
	fn := func(ctx context.Context) (any, error) { atomic.AddInt32(&runs, 1); return "ok", nil }
	pctx, pcancel := context.WithCancel(context.Background())
	primaryErr := make(chan error, 1)
	go func() { _, err := g.Do(pctx, context.Background(), "k", 0, fn); primaryErr <- err }()
	time.Sleep(10 * time.Millisecond)
	follower := make(chan any, 1)
	go func() { v, _ := g.Do(context.Background(), context.Background(), "k", 0, fn); follower <- v }()
	time.Sleep(10 * time.Millisecond)

	pcancel() // the primary's client disconnects while still queued
	if err := <-primaryErr; !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("primary: want ErrQueueTimeout, got %v", err)
	}
	close(release)
	if v := <-follower; v != "ok" {
		t.Fatalf("follower must still get the render, got %v", v)
	}
	if atomic.LoadInt32(&runs) != 1 {
		t.Fatalf("runs=%d want 1", runs)
	}
}

// When every waiter has left before a slot frees up, the run is dropped without taking a slot.
func TestDo_AbandonedQueuedCallNeverRuns(t *testing.T) {
	g := New(1)
	release := make(chan struct{})
	go g.Do(context.Background(), context.Background(), "busy", 0, func(ctx context.Context) (any, error) { <-release; return nil, nil }) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)

	var runs int32
	fn := func(ctx context.Context) (any, error) { atomic.AddInt32(&runs, 1); return "stale", nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = g.Do(ctx, context.Background(), "k", 0, fn) })
	}
	wg.Wait()
	if g.InFlight() != 1 {
		t.Fatalf("abandoned key must be removed at once: InFlight=%d", g.InFlight())
	}

	// A fresh request for the same key starts a new run instead of joining the abandoned one
	fresh := make(chan any, 1)
	go func() {
		v, _ := g.Do(context.Background(), context.Background(), "k", 0, func(ctx context.Context) (any, error) { return "fresh", nil })
		fresh <- v
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	if v := <-fresh; v != "fresh" {
		t.Fatalf("want fresh, got %v", v)
	}
	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&runs) != 0 {
		t.Fatalf("abandoned run must never execute: runs=%d", runs)
	}
	if g.InFlight() != 0 {
		t.Fatalf("InFlight=%d", g.InFlight())
	}
}

// Leaving at the same moment a slot is taken must never leak the slot.
func TestDo_LeaveRacingSlotAcquisitionNeverLeaksSlots(t *testing.T) {
	g := New(1)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for range 500 {
		_, _ = g.Do(cancelled, context.Background(), "k", 0, func(ctx context.Context) (any, error) { return nil, nil })
	}
	deadline := time.Now().Add(time.Second)
	for g.InFlight() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	v, err := g.Do(context.Background(), context.Background(), "after", 0, func(ctx context.Context) (any, error) { return "ok", nil })
	if err != nil || v != "ok" || len(g.slots) != 0 {
		t.Fatalf("slot leaked: v=%v err=%v used=%d", v, err, len(g.slots))
	}
}

func TestStats_RunningAndWaiting(t *testing.T) {
	g := New(1)
	release := make(chan struct{})
	slow := func(ctx context.Context) (any, error) { <-release; return nil, nil }
	go g.Do(context.Background(), context.Background(), "a", 0, slow) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)
	go g.Do(context.Background(), context.Background(), "b", 0, slow) //nolint:errcheck
	go g.Do(context.Background(), context.Background(), "c", 0, slow) //nolint:errcheck
	go g.Do(context.Background(), context.Background(), "c", 0, slow) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)
	if running, waiting := g.Stats(); running != 1 || waiting != 2 {
		t.Fatalf("want 1 running / 2 waiting keys, got %d / %d", running, waiting)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	if running, waiting := g.Stats(); running != 0 || waiting != 0 {
		t.Fatalf("drained: %d / %d", running, waiting)
	}
}

// The queue timeout bounds only the wait for a slot: a render that started is awaited to completion.
func TestDo_QueueTimeoutDoesNotCutAStartedRender(t *testing.T) {
	g := New(1)
	release := make(chan struct{})
	go g.Do(context.Background(), context.Background(), "busy", 0, func(ctx context.Context) (any, error) { <-release; return nil, nil }) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		v, err := g.Do(context.Background(), context.Background(), "k", 60*time.Millisecond, func(ctx context.Context) (any, error) {
			time.Sleep(120 * time.Millisecond) // longer than the queue timeout
			return "ok", nil
		})
		if err == nil && v != "ok" {
			err = errors.New("wrong value")
		}
		done <- err
	}()
	time.Sleep(30 * time.Millisecond) // queued for 30ms of the 60ms budget
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("a started render must not be cut by the queue timeout: %v", err)
	}
}

func TestDo_QueueTimeoutFiresWhileWaitingForASlot(t *testing.T) {
	g := New(1)
	release := make(chan struct{})
	defer close(release)
	go g.Do(context.Background(), context.Background(), "busy", 0, func(ctx context.Context) (any, error) { <-release; return nil, nil }) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	_, err := g.Do(context.Background(), context.Background(), "k", 30*time.Millisecond, func(ctx context.Context) (any, error) { return "never", nil })
	if !errors.Is(err, ErrQueueTimeout) || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("want ErrQueueTimeout after ~30ms, got %v after %v", err, time.Since(start))
	}
}

// A follower that joins a running render is not subject to the queue timeout either.
func TestDo_FollowerOfARunningRenderIgnoresQueueTimeout(t *testing.T) {
	g := New(2)
	release := make(chan struct{})
	fn := func(ctx context.Context) (any, error) { <-release; return "ok", nil }
	go g.Do(context.Background(), context.Background(), "k", 0, fn) //nolint:errcheck
	time.Sleep(10 * time.Millisecond)
	got := make(chan error, 1)
	go func() {
		_, err := g.Do(context.Background(), context.Background(), "k", 20*time.Millisecond, fn)
		got <- err
	}()
	time.Sleep(60 * time.Millisecond)
	close(release)
	if err := <-got; err != nil {
		t.Fatalf("follower of a running render: %v", err)
	}
}

// A queue timeout firing as the run takes its slot either times out cleanly or returns the result.
func TestDo_QueueTimeoutRacingStartIsConsistent(t *testing.T) {
	g := New(1)
	for range 500 {
		v, err := g.Do(context.Background(), context.Background(), "k", time.Nanosecond, func(ctx context.Context) (any, error) { return "ok", nil })
		if err == nil && v != "ok" || err != nil && !errors.Is(err, ErrQueueTimeout) {
			t.Fatalf("v=%v err=%v", v, err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for g.InFlight() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if v, err := g.Do(context.Background(), context.Background(), "after", 0, func(ctx context.Context) (any, error) { return "ok", nil }); err != nil || v != "ok" {
		t.Fatalf("gate unusable: %v %v", v, err)
	}
}
