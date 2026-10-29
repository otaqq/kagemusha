package lifecycle

import (
	"testing"
	"time"
)

func TestPolicy_ExitsAfterMaxRenders(t *testing.T) {
	p := newPolicy(3, 0, 0)
	if r := p.Record(); r != ExitNone {
		t.Fatal(r)
	}
	p.Record()
	if r := p.Record(); r != ExitMaxRenders {
		t.Fatalf("want max_renders, got %q", r)
	}
	if p.Renders() != 3 {
		t.Fatal(p.Renders())
	}
}

func TestPolicy_ExitsAfterMaxAge(t *testing.T) {
	p := newPolicy(0, time.Hour, 0)
	base := p.started
	p.now = func() time.Time { return base.Add(59 * time.Minute) }
	if r := p.Check(); r != ExitNone {
		t.Fatal(r)
	}
	p.now = func() time.Time { return base.Add(time.Hour) }
	if r := p.Check(); r != ExitMaxAge {
		t.Fatalf("want max_age, got %q", r)
	}
	if r := p.Record(); r != ExitMaxAge {
		t.Fatalf("Record should also report age exit, got %q", r)
	}
}

func TestPolicy_ZeroLimitsDisable(t *testing.T) {
	p := newPolicy(0, 0, 0)
	for i := 0; i < 100; i++ {
		if r := p.Record(); r != ExitNone {
			t.Fatal(r)
		}
	}
}

func TestExitReason_ExitCode(t *testing.T) {
	if ExitMaxRenders.ExitCode() != 0 || ExitMaxAge.ExitCode() != 0 || ExitNone.ExitCode() != 1 {
		t.Fatal("exit code mapping")
	}
}

func TestNewPolicy_JitterOnlyShortensLimits(t *testing.T) {
	for _, r := range []float64{0, 0.5, 0.999} {
		p := newPolicy(1000, 6*time.Hour, r)
		wantRenders := int(1000 * (1 - workerJitter*r))
		wantAge := time.Duration(float64(6*time.Hour) * (1 - workerJitter*r))
		if p.MaxRenders != wantRenders || p.MaxAge != wantAge {
			t.Fatalf("r=%v: got %d %v, want %d %v", r, p.MaxRenders, p.MaxAge, wantRenders, wantAge)
		}
		if p.MaxRenders > 1000 || p.MaxRenders < 800 || p.MaxAge > 6*time.Hour || p.MaxAge < 288*time.Minute {
			t.Fatalf("r=%v: limits outside [80%%, 100%%]: %d %v", r, p.MaxRenders, p.MaxAge)
		}
	}
}

func TestNewPolicy_JitterKeepsSmallAndDisabledLimits(t *testing.T) {
	if p := newPolicy(0, 0, 0.9); p.MaxRenders != 0 || p.MaxAge != 0 {
		t.Fatalf("disabled limits must stay disabled: %d %v", p.MaxRenders, p.MaxAge)
	}
	if p := newPolicy(1, time.Minute, 0.999); p.MaxRenders != 1 {
		t.Fatalf("a positive limit never drops to 0 (which would disable it): %d", p.MaxRenders)
	}
}

func TestNewPolicy_WorkersDoNotShareLimits(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 20 {
		seen[NewPolicy(1000, 6*time.Hour).MaxAge] = true
	}
	if len(seen) < 2 {
		t.Fatal("workers started together must get different limits")
	}
}
