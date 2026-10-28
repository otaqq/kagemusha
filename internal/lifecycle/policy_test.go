package lifecycle

import (
	"testing"
	"time"
)

func TestPolicy_ExitsAfterMaxRenders(t *testing.T) {
	p := NewPolicy(3, 0)
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
	p := NewPolicy(0, time.Hour)
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
	p := NewPolicy(0, 0)
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
