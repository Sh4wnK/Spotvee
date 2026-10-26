package store

import (
	"container/list"
	"sync"
)

// LRU is a goroutine-safe LRU cache with both entry-count and total-byte
// bounds. It is used for album-art bytes so long-running syncs never grow
// without limit.
type LRU[V any] struct {
	mu      sync.Mutex
	maxEnt  int
	maxByte int64
	ll      *list.List
	items   map[string]*list.Element
	bytes   int64
}

type entry[V any] struct {
	key   string
	value V
	bytes int64
}

// NewLRU builds a cache capped at maxEntries entries and maxBytes total
// value bytes (per-entry byte counts must be supplied by callers).
func NewLRU[V any](maxEntries int, maxBytes int64) *LRU[V] {
	return &LRU[V]{
		maxEnt:  maxEntries,
		maxByte: maxBytes,
		ll:      list.New(),
		items:   make(map[string]*list.Element),
	}
}

// Get returns the cached value if present and moves it to the front.
func (c *LRU[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry[V]).value, true
}

// Set inserts or replaces key, evicting LRU entries until the bounds hold.
func (c *LRU[V]) Set(key string, value V, byteCount int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry[V])
		c.bytes += byteCount - e.bytes
		e.value = value
		e.bytes = byteCount
		c.ll.MoveToFront(el)
	} else {
		e := &entry[V]{key: key, value: value, bytes: byteCount}
		el := c.ll.PushFront(e)
		c.items[key] = el
		c.bytes += byteCount
	}
	for c.bytes > c.maxByte || c.ll.Len() > c.maxEnt {
		back := c.ll.Back()
		if back == nil {
			break
		}
		e := back.Value.(*entry[V])
		c.ll.Remove(back)
		delete(c.items, e.key)
		c.bytes -= e.bytes
	}
}

// Len returns the current entry count.
func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Bytes returns the current total byte count.
func (c *LRU[V]) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}
