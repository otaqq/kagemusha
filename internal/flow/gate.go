// Package flow provides flow control:
// single-flight (identical keys share one run) → concurrency cap → FIFO queue with a deadline.
package flow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrQueueTimeout means no slot freed up within the queue timeout, or the caller gave up. Callers map it to 503 or similar.
var ErrQueueTimeout = errors.New("flow: queue wait timed out")

// Gate is the flow controller.
type Gate struct {
	slots    chan struct{}
	mu       sync.Mutex
	calls    map[string]*call
	progress atomic.Int64 // unix nanos of the last slot taken or released
}

type call struct {
	done chan struct{}
	val  any
	err  error

	// Guarded by Gate.mu. waiters counts callers still waiting; when it drops to zero before
	// the run starts, abandoned is closed and the queued run is dropped without taking a slot.
	waiters   int
	started   bool
	startedCh chan struct{} // closed when the run takes a slot
	abandoned chan struct{}
}

// New creates a Gate that runs at most max tasks at once.
func New(max int) *Gate {
	if max < 1 {
		max = 1
	}
	g := &Gate{slots: make(chan struct{}, max), calls: map[string]*call{}}
	g.touch()
	return g
}

// Do runs fn for key. Callers with the same key in flight share its result; fn runs once.
// Without a free slot it waits FIFO; queueTimeout (0 = none) bounds only that wait, never a run
// that already started. ctx is the caller: when it ends the caller stops waiting (ErrQueueTimeout).
// A queued run continues as long as any caller still waits for it, so a disconnecting primary
// does not fail its followers. fn runs with runCtx, not a caller's ctx, so disconnects never cancel the render.
func (g *Gate) Do(ctx context.Context, runCtx context.Context, key string, queueTimeout time.Duration, fn func(context.Context) (any, error)) (any, error) {
	g.mu.Lock()
	if c, ok := g.calls[key]; ok {
		c.waiters++
		g.mu.Unlock()
		return g.wait(ctx, key, c, queueTimeout)
	}
	c := &call{done: make(chan struct{}), waiters: 1, startedCh: make(chan struct{}), abandoned: make(chan struct{})}
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
		close(c.startedCh)
		g.mu.Unlock()
		g.touch()
		defer func() { <-g.slots; g.touch() }()
		defer func() {
			if r := recover(); r != nil {
				c.err = errors.New("flow: render panicked")
			}
		}()
		c.val, c.err = fn(runCtx)
	}()
	return g.wait(ctx, key, c, queueTimeout)
}

func (g *Gate) wait(ctx context.Context, key string, c *call, queueTimeout time.Duration) (any, error) {
	var timeout <-chan time.Time
	if queueTimeout > 0 {
		t := time.NewTimer(queueTimeout)
		defer t.Stop()
		timeout = t.C
	}
	started := c.startedCh
	for {
		select {
		case <-c.done:
			return c.val, c.err
		case <-started: // the run took a slot: the queue timeout no longer applies
			started, timeout = nil, nil
		case <-timeout:
			if g.leaveQueued(key, c) {
				return nil, ErrQueueTimeout
			}
			timeout = nil // started just now
		case <-ctx.Done():
			g.mu.Lock()
			g.leaveLocked(key, c)
			g.mu.Unlock()
			return nil, ErrQueueTimeout
		}
	}
}

// leaveQueued drops one waiter only if the run has not started yet.
func (g *Gate) leaveQueued(key string, c *call) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.started {
		return false
	}
	g.leaveLocked(key, c)
	return true
}

// leaveLocked drops one waiter. The last waiter of a run that has not started abandons it,
// and the key is freed at once so a new request starts a fresh run.
func (g *Gate) leaveLocked(key string, c *call) {
	c.waiters--
	if c.waiters == 0 && !c.started {
		close(c.abandoned)
		if g.calls[key] == c {
			delete(g.calls, key)
		}
	}
}

func (g *Gate) touch() { g.progress.Store(time.Now().UnixNano()) }

// Stalled reports whether every slot has been busy for at least d with no run starting or finishing
// (renders are bounded by their own deadlines, so this means something hung). d <= 0 disables the check.
func (g *Gate) Stalled(d time.Duration) bool {
	return d > 0 && len(g.slots) == cap(g.slots) && time.Since(time.Unix(0, g.progress.Load())) >= d
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
