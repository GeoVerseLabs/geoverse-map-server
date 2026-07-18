// Package cache provides a small in-process LRU cache with TTL, used to
// memoize rendered tiles. It has no external dependencies.
package cache

import (
	"container/list"
	"sync"
	"time"
)

type entry struct {
	key     string
	value   []byte
	expires time.Time
}

// Cache is a thread-safe LRU cache with per-entry TTL.
type Cache struct {
	mu         sync.Mutex
	maxEntries int
	ttl        time.Duration
	ll         *list.List
	items      map[string]*list.Element
}

// New creates a cache holding at most maxEntries items for at most ttl each.
// A maxEntries <= 0 disables the cache (all operations become no-ops).
func New(maxEntries int, ttl time.Duration) *Cache {
	return &Cache{
		maxEntries: maxEntries,
		ttl:        ttl,
		ll:         list.New(),
		items:      make(map[string]*list.Element),
	}
}

// Get returns the cached value for key, if present and not expired.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c == nil || c.maxEntries <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	en := el.Value.(*entry)
	if c.ttl > 0 && time.Now().After(en.expires) {
		c.ll.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return en.value, true
}

// Set stores value under key, evicting the least recently used entry if full.
func (c *Cache) Set(key string, value []byte) {
	if c == nil || c.maxEntries <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		en := el.Value.(*entry)
		en.value = value
		en.expires = time.Now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&entry{key: key, value: value, expires: time.Now().Add(c.ttl)})
	c.items[key] = el
	for c.ll.Len() > c.maxEntries {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.items, last.Value.(*entry).key)
	}
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
