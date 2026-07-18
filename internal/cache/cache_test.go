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
