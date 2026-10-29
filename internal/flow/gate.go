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

	// Guarded by Gate.mu. waiters counts callers still waiting; when it drops to zero before
	// the run starts, abandoned is closed and the queued run is dropped without taking a slot.
	waiters   int
	started   bool
	abandoned chan struct{}
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
// A queued run continues as long as any caller still waits for it, so a disconnecting primary
// does not fail its followers. fn runs with runCtx, not a caller's ctx, so disconnects never cancel the render.
func (g *Gate) Do(ctx context.Context, runCtx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		c.waiters++
		g.mu.Unlock()
		return g.wait(ctx, key, c)
	}
	c := &call{done: make(chan struct{}), waiters: 1, abandoned: make(chan struct{})}
	g.calls[key] = c
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			if g.calls[key] == c {
				delete(g.calls, key)
			}
			g.mu.Unlock()
			close(c.done)
		}()
		select {
		case g.slots <- struct{}{}:
		case <-c.abandoned:
			c.err = ErrQueueTimeout
			return
		}
		g.mu.Lock()
		if c.waiters == 0 { // abandoned while the slot was being taken
			g.mu.Unlock()
			<-g.slots
			c.err = ErrQueueTimeout
			return
		}
		c.started = true
		g.mu.Unlock()
		defer func() { <-g.slots }()
		defer func() {
			if r := recover(); r != nil {
				c.err = errors.New("flow: render panicked")
			}
		}()
		c.val, c.err = fn(runCtx)
	}()
	return g.wait(ctx, key, c)
}

func (g *Gate) wait(ctx context.Context, key string, c *call) (any, error) {
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		g.leave(key, c)
		return nil, ErrQueueTimeout
	}
}

// leave drops one waiter. The last waiter of a run that has not started abandons it,
// and the key is freed at once so a new request starts a fresh run.
func (g *Gate) leave(key string, c *call) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c.waiters--
	if c.waiters == 0 && !c.started {
		close(c.abandoned)
		if g.calls[key] == c {
			delete(g.calls, key)
		}
	}
}

// Stats returns the number of runs holding a slot and the number of keys waiting for one (for metrics).
func (g *Gate) Stats() (running, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if !c.started {
			waiting++
		}
	}
	return len(g.slots), waiting
}

// InFlight returns the number of keys running or queued (for metrics).
func (g *Gate) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}
