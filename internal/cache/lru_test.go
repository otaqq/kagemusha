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

func newBytesLRU(c *clock, maxEntries int, maxBytes int64) *LRU[string] {
	return New[string](time.Minute, maxEntries, c.now).LimitBytes(maxBytes, func(v string) int { return len(v) })
}

func TestLRU_EvictsLeastRecentlyUsedToStayUnderMaxBytes(t *testing.T) {
	l := newBytesLRU(newClock(), 100, 10)
	l.Set("a", "aaaa") // 4
	l.Set("b", "bbbb") // 8
	l.Get("a")         // a is the most recently used
	l.Set("c", "cccc") // 12 > 10: evict b
	if _, _, ok := l.Get("b"); ok {
		t.Fatal("b must be evicted to stay under max bytes")
	}
	if _, _, ok := l.Get("a"); !ok {
		t.Fatal("a must survive")
	}
	if l.Len() != 2 || l.Bytes() != 8 {
		t.Fatalf("len=%d bytes=%d", l.Len(), l.Bytes())
	}
}

func TestLRU_EntryLargerThanMaxBytesIsNotCached(t *testing.T) {
	l := newBytesLRU(newClock(), 100, 10)
	l.Set("a", "aaaa")
	l.Set("huge", "xxxxxxxxxxx") // 11 > 10
	if _, _, ok := l.Get("huge"); ok {
		t.Fatal("an entry over the limit must not be cached")
	}
	if _, _, ok := l.Get("a"); !ok || l.Bytes() != 4 {
		t.Fatalf("existing entries must stay: bytes=%d", l.Bytes())
	}
}

func TestLRU_BytesFollowOverwriteExpiryAndEviction(t *testing.T) {
	c := newClock()
	l := newBytesLRU(c, 2, 100)
	l.Set("a", "aaaa")
	l.Set("a", "aa") // overwrite shrinks
	if l.Bytes() != 2 {
		t.Fatalf("overwrite: bytes=%d", l.Bytes())
	}
	l.Set("b", "bbb")
	l.Set("c", "c") // entry limit evicts a
	if l.Bytes() != 4 {
		t.Fatalf("entry-limit eviction: bytes=%d", l.Bytes())
	}
	c.add(time.Minute)
	l.Get("b")
	if l.Bytes() != 1 {
		t.Fatalf("expiry: bytes=%d", l.Bytes())
	}
	l.Set("big", "x")
	l.Set("big", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx") // >100
	if _, _, ok := l.Get("big"); ok || l.Bytes() != 1 {
		t.Fatalf("an overwrite over the limit drops the old value too: bytes=%d", l.Bytes())
	}
}

func TestLRU_NoByteLimitByDefault(t *testing.T) {
	l := New[string](time.Minute, 10, nil)
	l.Set("a", "aaaa")
	if l.Bytes() != 0 {
		t.Fatal("without LimitBytes sizes are not tracked")
	}
}
