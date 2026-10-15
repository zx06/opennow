// Package cache provides a small, concurrency-safe LRU cache with an optional
// TTL and a cost budget (for example total bytes). Remote storage backends use
// it to keep request counts low for metadata and hot files.
package cache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// LRU is a least-recently-used cache. A zero TTL means entries never expire
// (useful for immutable, content-addressed data). Entries are evicted when the
// total cost exceeds MaxCost.
type LRU[K comparable, V any] struct {
	mu      sync.Mutex
	ll      *list.List
	items   map[K]*list.Element
	maxCost int64
	cost    int64
	ttl     time.Duration
	costFn  func(V) int64
	now     func() time.Time

	hits, misses atomic.Int64
}

type entry[K comparable, V any] struct {
	key     K
	val     V
	cost    int64
	expires time.Time
}

// New creates a cache. costFn may be nil, in which case every entry costs 1
// and maxCost is an entry count.
func New[K comparable, V any](maxCost int64, ttl time.Duration, costFn func(V) int64) *LRU[K, V] {
	if maxCost <= 0 {
		maxCost = 1
	}
	return &LRU[K, V]{
		ll:      list.New(),
		items:   map[K]*list.Element{},
		maxCost: maxCost,
		ttl:     ttl,
		costFn:  costFn,
		now:     time.Now,
	}
}

// SetClock overrides the time source (tests).
func (c *LRU[K, V]) SetClock(now func() time.Time) { c.now = now }

// Get returns a cached value if present and not expired.
func (c *LRU[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	el, ok := c.items[k]
	if !ok {
		c.misses.Add(1)
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if !e.expires.IsZero() && c.now().After(e.expires) {
		c.removeElement(el)
		c.misses.Add(1)
		return zero, false
	}
	c.ll.MoveToFront(el)
	c.hits.Add(1)
	return e.val, true
}

// Set stores a value using the cache's default TTL.
func (c *LRU[K, V]) Set(k K, v V) { c.SetTTL(k, v, c.ttl) }

// SetTTL stores a value with an explicit TTL (0 = no expiry).
func (c *LRU[K, V]) SetTTL(k K, v V, ttl time.Duration) {
	cost := int64(1)
	if c.costFn != nil {
		cost = c.costFn(v)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cost > c.maxCost {
		// Never cache something larger than the whole budget.
		if el, ok := c.items[k]; ok {
			c.removeElement(el)
		}
		return
	}
	var exp time.Time
	if ttl > 0 {
		exp = c.now().Add(ttl)
	}
	if el, ok := c.items[k]; ok {
		e := el.Value.(*entry[K, V])
		c.cost += cost - e.cost
		e.val, e.cost, e.expires = v, cost, exp
		c.ll.MoveToFront(el)
	} else {
		el := c.ll.PushFront(&entry[K, V]{key: k, val: v, cost: cost, expires: exp})
		c.items[k] = el
		c.cost += cost
	}
	for c.cost > c.maxCost {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.removeElement(back)
	}
}

// Delete removes a key.
func (c *LRU[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.removeElement(el)
	}
}

// DeleteFunc removes every key for which match returns true.
func (c *LRU[K, V]) DeleteFunc(match func(K) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, el := range c.items {
		if match(k) {
			c.removeElement(el)
		}
	}
}

// Purge removes everything.
func (c *LRU[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = map[K]*list.Element{}
	c.cost = 0
}

func (c *LRU[K, V]) removeElement(el *list.Element) {
	e := el.Value.(*entry[K, V])
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.cost -= e.cost
}

// Stats is a snapshot of cache counters.
type Stats struct {
	Entries int
	Cost    int64
	MaxCost int64
	Hits    int64
	Misses  int64
}

func (c *LRU[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Entries: len(c.items), Cost: c.cost, MaxCost: c.maxCost, Hits: c.hits.Load(), Misses: c.misses.Load()}
}
