// Package lifecycle manages process lifetimes.
// Voluntary worker exit (after N renders or T time) is part of the spec from the start.
package lifecycle

import (
	"sync/atomic"
	"time"
)

// ExitReason is why a worker exits. It maps to an exit code so operators can tell causes apart at a glance.
type ExitReason string

const (
	ExitNone       ExitReason = ""
	ExitMaxRenders ExitReason = "max_renders"
	ExitMaxAge     ExitReason = "max_age"
)

// ExitCode returns the exit code for the reason: 0 for a planned recycle, non-zero for crashes.
func (r ExitReason) ExitCode() int {
	switch r {
	case ExitMaxRenders, ExitMaxAge:
		return 0
	}
	return 1
}

// Policy decides when a worker exits voluntarily.
type Policy struct {
	MaxRenders int
	MaxAge     time.Duration
	started    time.Time
	renders    atomic.Int64
	now        func() time.Time
}

// NewPolicy creates a Policy that records the start time.
func NewPolicy(maxRenders int, maxAge time.Duration) *Policy {
	p := &Policy{MaxRenders: maxRenders, MaxAge: maxAge, now: time.Now}
	p.started = p.now()
	return p
}

// Record counts one finished render and returns a reason if the worker should exit.
func (p *Policy) Record() ExitReason {
	n := p.renders.Add(1)
	if p.MaxRenders > 0 && n >= int64(p.MaxRenders) {
		return ExitMaxRenders
	}
	return p.Check()
}

// Check decides on time-based exit (call it periodically, even without renders).
func (p *Policy) Check() ExitReason {
	if p.MaxAge > 0 && p.now().Sub(p.started) >= p.MaxAge {
		return ExitMaxAge
	}
	return ExitNone
}

// Renders returns the total number of renders (for metrics).
func (p *Policy) Renders() int64 { return p.renders.Load() }
