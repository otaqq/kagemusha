// Package flow provides flow control:
// single-flight (identical keys share one run) → concurrency cap → FIFO queue with a deadline.
package flow

import (
	"context"
	"errors"
	"sync"
)

// ErrQueueTimeout means the queue deadline passed. Callers map it to 503 or similar.
var ErrQueueTimeout = errors.New("flow: queue wait timed out")

// Gate is the flow controller.
type Gate struct {
	slots chan struct{}
	mu    sync.Mutex
	calls map[string]*call
}

type call struct {
	done chan struct{}
	val  any
	err  error
}

// New creates a Gate that runs at most max tasks at once.
func New(max int) *Gate {
	if max < 1 {
		max = 1
	}
	return &Gate{slots: make(chan struct{}, max), calls: map[string]*call{}}
}

// Do runs fn for key. Callers with the same key in flight share its result; fn runs once.
// Without a free slot it waits FIFO and returns ErrQueueTimeout when ctx (the queue deadline) ends.
// fn runs with runCtx, not the primary's ctx, so a client disconnect does not cancel the render.
func (g *Gate) Do(ctx context.Context, runCtx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		return g.wait(ctx, c)
	}
	c := &call{done: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
			close(c.done)
		}()
		select {
		case g.slots <- struct{}{}:
		case <-ctx.Done():
			// The primary gave up in the queue. fn never started, so followers get the same result
			c.err = ErrQueueTimeout
			return
		}
		defer func() { <-g.slots }()
		defer func() {
			if r := recover(); r != nil {
				c.err = errors.New("flow: render panicked")
			}
		}()
		c.val, c.err = fn(runCtx)
	}()
	return g.wait(ctx, c)
}

func (g *Gate) wait(ctx context.Context, c *call) (any, error) {
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		return nil, ErrQueueTimeout
	}
}

// InFlight returns the number of keys running or queued (for metrics).
func (g *Gate) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}
