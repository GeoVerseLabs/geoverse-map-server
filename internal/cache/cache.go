// Package cache provides a small in-process LRU cache with TTL, used to
// memoize rendered tiles. It has no external dependencies.
package cache

import (
	"container/list"
	"sync"
	"sync/atomic"
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

	// Counters are atomic rather than guarded by mu so that Stats stays
	// cheap enough to be called from /metrics scrapes on a hot server.
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64
	bytes     atomic.Int64
}

// MemStats is a point-in-time snapshot of the in-memory tier.
type MemStats struct {
	Entries    int     `json:"entries"`
	MaxEntries int     `json:"maxEntries"`
	Bytes      int64   `json:"bytes"`
	Hits       uint64  `json:"hits"`
	Misses     uint64  `json:"misses"`
	Evictions  uint64  `json:"evictions"`
	HitRate    float64 `json:"hitRate"`
	TTLSeconds float64 `json:"ttlSeconds"`
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
		c.misses.Add(1)
		return nil, false
	}
	en := el.Value.(*entry)
	if c.ttl > 0 && time.Now().After(en.expires) {
		c.ll.Remove(el)
		delete(c.items, key)
		c.bytes.Add(-int64(len(en.value)))
		c.misses.Add(1)
		return nil, false
	}
	c.ll.MoveToFront(el)
	c.hits.Add(1)
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
		c.bytes.Add(int64(len(value)) - int64(len(en.value)))
		en.value = value
		en.expires = time.Now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&entry{key: key, value: value, expires: time.Now().Add(c.ttl)})
	c.items[key] = el
	c.bytes.Add(int64(len(value)))
	for c.ll.Len() > c.maxEntries {
		last := c.ll.Back()
		c.ll.Remove(last)
		lastEn := last.Value.(*entry)
		delete(c.items, lastEn.key)
		c.bytes.Add(-int64(len(lastEn.value)))
		c.evictions.Add(1)
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

// Purge drops every entry and returns how many were removed. Hit/miss
// counters survive on purpose: they describe the process lifetime, and
// resetting them on a cache flush would hide the very regression an
// operator flushes the cache to investigate.
func (c *Cache) Purge() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.ll.Len()
	c.ll.Init()
	c.items = make(map[string]*list.Element)
	c.bytes.Store(0)
	return n
}

// Stats snapshots the in-memory tier.
func (c *Cache) Stats() MemStats {
	if c == nil {
		return MemStats{}
	}
	hits, misses := c.hits.Load(), c.misses.Load()
	s := MemStats{
		Entries:    c.Len(),
		MaxEntries: c.maxEntries,
		Bytes:      c.bytes.Load(),
		Hits:       hits,
		Misses:     misses,
		Evictions:  c.evictions.Load(),
		TTLSeconds: c.ttl.Seconds(),
	}
	if total := hits + misses; total > 0 {
		s.HitRate = float64(hits) / float64(total)
	}
	return s
}
