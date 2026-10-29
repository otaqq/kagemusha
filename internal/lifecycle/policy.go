// Package lifecycle manages process lifetimes.
// Voluntary worker exit (after N renders or T time) is part of the spec from the start.
package lifecycle

import (
	"math/rand/v2"
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

// workerJitter is how much earlier than the configured limits a worker may exit, so workers
// started together (a deploy, a scale-out) do not all recycle at once. Limits are never exceeded.
const workerJitter = 0.2

// NewPolicy creates a Policy that records the start time. Each limit is shortened by a random
// 0–20%, so MaxRenders and MaxAge hold the effective values.
func NewPolicy(maxRenders int, maxAge time.Duration) *Policy {
	return newPolicy(maxRenders, maxAge, rand.Float64())
}

func newPolicy(maxRenders int, maxAge time.Duration, r float64) *Policy {
	f := 1 - workerJitter*r
	if maxRenders > 0 {
		maxRenders = max(1, int(float64(maxRenders)*f))
	}
	p := &Policy{MaxRenders: maxRenders, MaxAge: time.Duration(float64(maxAge) * f), now: time.Now}
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
