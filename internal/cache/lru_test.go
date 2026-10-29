package cache

import (
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time             { return c.t }
func (c *clock) add(d time.Duration)        { c.t = c.t.Add(d) }
func newClock() *clock                      { return &clock{t: time.Unix(1_700_000_000, 0)} }
func newLRU(c *clock, max int) *LRU[string] { return New[string](time.Minute, max, c.now) }

func TestLRU_GetReturnsValueAndAge(t *testing.T) {
	c := newClock()
	l := newLRU(c, 2)
	if _, _, ok := l.Get("a"); ok {
		t.Fatal("empty cache must miss")
	}
	l.Set("a", "A")
	c.add(10 * time.Second)
	v, age, ok := l.Get("a")
	if !ok || v != "A" || age != 10*time.Second {
		t.Fatalf("got %q %v %v", v, age, ok)
	}
}

func TestLRU_ExpiredEntriesMissAndAreRemoved(t *testing.T) {
	c := newClock()
	l := newLRU(c, 2)
	l.Set("a", "A")
	c.add(time.Minute)
	if _, _, ok := l.Get("a"); ok || l.Len() != 0 {
		t.Fatalf("expired entry must miss and be removed (len=%d)", l.Len())
	}
}

func TestLRU_EvictsLeastRecentlyUsed(t *testing.T) {
	c := newClock()
	l := newLRU(c, 2)
	l.Set("a", "A")
	l.Set("b", "B")
	l.Get("a") // a is now the most recently used
	l.Set("c", "C")
	if _, _, ok := l.Get("b"); ok {
		t.Fatal("b must be evicted")
	}
	if _, _, ok := l.Get("a"); !ok {
		t.Fatal("a must survive")
	}
	if l.Len() != 2 {
		t.Fatalf("len=%d", l.Len())
	}
}

func TestLRU_SetOverwritesAndResetsAge(t *testing.T) {
	c := newClock()
	l := newLRU(c, 2)
	l.Set("a", "old")
	c.add(50 * time.Second)
	l.Set("a", "new")
	c.add(20 * time.Second)
	v, age, ok := l.Get("a")
	if !ok || v != "new" || age != 20*time.Second || l.Len() != 1 {
		t.Fatalf("got %q %v %v len=%d", v, age, ok, l.Len())
	}
}

func TestLRU_DefaultsAndConcurrentUse(t *testing.T) {
	l := New[int](time.Minute, 0, nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() { l.Set("k", i); l.Get("k") })
	}
	wg.Wait()
	if l.Len() != 1 {
		t.Fatalf("max<1 must become 1, len=%d", l.Len())
	}
}
