// Package cache provides a small in-process LRU cache with a TTL for rendered pages.
// It absorbs bursts right after a render (bots retrying after their own timeout); the CDN remains the main cache.
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

	mu    sync.Mutex
	order *list.List // front = most recently used
	items map[string]*list.Element
}

type entry[V any] struct {
	key    string
	val    V
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
		c.order.Remove(el)
		delete(c.items, key)
		return zero, 0, false
	}
	c.order.MoveToFront(el)
	return e.val, age, true
}

// Set stores val for key, evicting the least recently used entry when full.
func (c *LRU[V]) Set(key string, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value = &entry[V]{key: key, val: val, stored: c.now()}
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&entry[V]{key: key, val: val, stored: c.now()})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*entry[V]).key)
	}
}

// Len returns the number of entries, expired ones included until they are looked up or evicted.
func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
