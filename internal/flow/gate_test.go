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
			results[i], _ = g.Do(context.Background(), context.Background(), "u1", fn)
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
	go g.Do(context.Background(), context.Background(), "a", slow) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := g.Do(ctx, context.Background(), "b", func(ctx context.Context) (any, error) { return 2, nil })
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
	go func() { v, _ := g.Do(context.Background(), context.Background(), "k", fn); primaryDone <- v }()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Do(ctx, context.Background(), "k", fn); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("follower: want timeout, got %v", err)
	}
	close(release)
	if v := <-primaryDone; v != "ok" {
		t.Fatalf("primary=%v", v)
	}
}

func TestDo_PanicBecomesErrorWithoutAffectingOthers(t *testing.T) {
	g := New(1)
	_, err := g.Do(context.Background(), context.Background(), "p", func(ctx context.Context) (any, error) { panic("boom") })
	if err == nil {
		t.Fatal("want error")
	}
	v, err := g.Do(context.Background(), context.Background(), "q", func(ctx context.Context) (any, error) { return "fine", nil })
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
	go g.Do(ctx, context.Background(), "r", fn) //nolint:errcheck
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
