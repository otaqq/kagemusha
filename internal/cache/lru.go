// Package cache provides a small in-process LRU cache with a TTL for rendered pages.
// It absorbs bursts right after a render (bots retrying after their own timeout); the CDN remains the main cache.
// Entries, total size and age are all bounded, so the cache cannot grow into an OOM kill.
package cache

import (
	"container/list"
	"sync"
	"time"
)

// LRU is a fixed-size cache whose entries expire after ttl. It is safe for concurrent use.
type LRU[V any] struct {
	ttl time.Duration
	max int
	now func() time.Time

	maxBytes int64
	size     func(V) int // nil: sizes are not tracked

	mu    sync.Mutex
	order *list.List // front = most recently used
	items map[string]*list.Element
	bytes int64
}

type entry[V any] struct {
	key    string
	val    V
	size   int
	stored time.Time
}

// New creates an LRU holding at most max entries for ttl each. A nil now uses time.Now.
func New[V any](ttl time.Duration, max int, now func() time.Time) *LRU[V] {
	if now == nil {
		now = time.Now
	}
	if max < 1 {
		max = 1
	}
	return &LRU[V]{ttl: ttl, max: max, now: now, order: list.New(), items: map[string]*list.Element{}}
}

// LimitBytes also bounds the total size of the cached values: least recently used entries are evicted
// to stay under maxBytes, and a value larger than maxBytes is not cached. Call it before use.
func (c *LRU[V]) LimitBytes(maxBytes int64, size func(V) int) *LRU[V] {
	c.maxBytes, c.size = maxBytes, size
	return c
}

// Get returns the value for key and how long ago it was stored. Expired entries are removed.
func (c *LRU[V]) Get(key string) (V, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	el, ok := c.items[key]
	if !ok {
		return zero, 0, false
	}
	e := el.Value.(*entry[V])
	age := c.now().Sub(e.stored)
	if age >= c.ttl {
		c.remove(el)
		return zero, 0, false
	}
	c.order.MoveToFront(el)
	return e.val, age, true
}

// Set stores val for key, evicting least recently used entries while over the entry or byte limit.
func (c *LRU[V]) Set(key string, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
	e := &entry[V]{key: key, val: val, stored: c.now()}
	if c.size != nil {
		e.size = c.size(val)
		if int64(e.size) > c.maxBytes {
			return
		}
	}
	c.items[key] = c.order.PushFront(e)
	c.bytes += int64(e.size)
	for c.order.Len() > c.max || (c.size != nil && c.bytes > c.maxBytes) {
		c.remove(c.order.Back())
	}
}

func (c *LRU[V]) remove(el *list.Element) {
	e := el.Value.(*entry[V])
	c.order.Remove(el)
	delete(c.items, e.key)
	c.bytes -= int64(e.size)
}

// Bytes returns the total size of the cached values (0 without LimitBytes).
func (c *LRU[V]) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Len returns the number of entries, expired ones included until they are looked up or evicted.
func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
