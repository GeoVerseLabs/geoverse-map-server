package cache

import (
	"testing"
	"time"
)

func TestSetGet(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("a", []byte("1"))
	if v, ok := c.Get("a"); !ok || string(v) != "1" {
		t.Fatalf("Get(a) = %q, %v", v, ok)
	}
	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss for unknown key")
	}
}

func TestLRUEviction(t *testing.T) {
	c := New(2, time.Minute)
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Get("a") // a becomes most recent
	c.Set("c", []byte("3"))
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should have survived")
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}

func TestTTL(t *testing.T) {
	c := New(10, 10*time.Millisecond)
	c.Set("a", []byte("1"))
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("entry should have expired")
	}
}

func TestDisabled(t *testing.T) {
	c := New(0, time.Minute)
	c.Set("a", []byte("1"))
	if _, ok := c.Get("a"); ok {
		t.Fatal("disabled cache must not store")
	}
	var nilCache *Cache
	nilCache.Set("a", []byte("1")) // must not panic
	if _, ok := nilCache.Get("a"); ok {
		t.Fatal("nil cache must miss")
	}
}

func TestStatsCountsHitsMissesAndEvictions(t *testing.T) {
	c := New(2, time.Minute)
	c.Set("a", []byte("12345"))
	c.Set("b", []byte("67"))
	c.Get("a")              // hit
	c.Get("nope")           // miss
	c.Set("c", []byte("8")) // evicts LRU ("b")

	s := c.Stats()
	if s.Hits != 1 || s.Misses != 1 {
		t.Errorf("hits/misses = %d/%d, want 1/1", s.Hits, s.Misses)
	}
	if s.Evictions != 1 {
		t.Errorf("evictions = %d, want 1", s.Evictions)
	}
	if s.Entries != 2 || s.MaxEntries != 2 {
		t.Errorf("entries = %d/%d, want 2/2", s.Entries, s.MaxEntries)
	}
	// "a" (5B) + "c" (1B); "b" was evicted and must not still be counted.
	if s.Bytes != 6 {
		t.Errorf("bytes = %d, want 6", s.Bytes)
	}
	if s.HitRate != 0.5 {
		t.Errorf("hitRate = %v, want 0.5", s.HitRate)
	}
}

func TestPurgeEmptiesButKeepsCounters(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Get("a")

	if n := c.Purge(); n != 2 {
		t.Errorf("Purge = %d, want 2", n)
	}
	if c.Len() != 0 {
		t.Errorf("Len after purge = %d, want 0", c.Len())
	}
	s := c.Stats()
	if s.Bytes != 0 {
		t.Errorf("bytes after purge = %d, want 0", s.Bytes)
	}
	// Lifetime counters describe the process, not the current contents:
	// resetting them would erase the evidence an operator purged to inspect.
	if s.Hits != 1 {
		t.Errorf("hits after purge = %d, want 1 (counters must survive)", s.Hits)
	}
	if n := c.Purge(); n != 0 {
		t.Errorf("second Purge = %d, want 0 (idempotent)", n)
	}
}
