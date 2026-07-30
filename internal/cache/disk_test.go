package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
)

func TestDiskSetGet(t *testing.T) {
	d, err := NewDisk(t.TempDir(), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.Set("cities/6/52/24", []byte("tiledata"))
	v, ok := d.Get("cities/6/52/24")
	if !ok || string(v) != "tiledata" {
		t.Fatalf("Get = %q, %v", v, ok)
	}
	if _, ok := d.Get("cities/6/0/0"); ok {
		t.Fatal("expected miss")
	}
}

func TestDiskRejectsUnsafeKeys(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDisk(dir, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.Set("../escape", []byte("x"))
	d.Set("a/../../b", []byte("x"))
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("path traversal escaped the cache dir")
	}
	if _, ok := d.Get("../escape"); ok {
		t.Fatal("unsafe key must miss")
	}
}

func TestDiskTTLExpiry(t *testing.T) {
	d, err := NewDisk(t.TempDir(), 10*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.Set("k", []byte("v"))
	time.Sleep(20 * time.Millisecond)
	if _, ok := d.Get("k"); ok {
		t.Fatal("entry should have expired")
	}
}

func TestDiskSweepEnforcesSize(t *testing.T) {
	dir := t.TempDir()
	// maxBytes = 1 MiB; write ~1.5 MiB in 3 files.
	d, err := NewDisk(dir, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	half := make([]byte, 512*1024)
	d.Set("a", half)
	// Ensure distinct mtimes so eviction order is deterministic.
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(filepath.Join(dir, "a"), old, old)
	d.Set("b", half)
	d.Set("c", half)

	d.Sweep()
	if _, ok := d.Get("a"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := d.Get("c"); !ok {
		t.Fatal("newest entry should survive")
	}
}

func TestTieredPromotion(t *testing.T) {
	cfg := config.Cache{
		Enabled:    true,
		MaxEntries: 10,
		TTL:        time.Minute,
		Disk:       config.DiskCache{Enabled: true, Dir: t.TempDir(), TTL: time.Hour},
	}
	tc, err := NewTiered(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	tc.Set("k", []byte("v"))
	// Simulate process restart: fresh memory tier, same disk tier.
	tc.mem = New(10, time.Minute)
	v, ok := tc.Get("k")
	if !ok || string(v) != "v" {
		t.Fatalf("disk tier lost the entry: %q, %v", v, ok)
	}
	// After promotion the memory tier must hold it too.
	if _, ok := tc.mem.Get("k"); !ok {
		t.Fatal("hit was not promoted to memory")
	}
}

func TestTieredDisabled(t *testing.T) {
	tc, err := NewTiered(config.Cache{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if tc != nil {
		t.Fatal("disabled cache must be nil")
	}
	tc.Set("k", []byte("v")) // nil receiver must not panic
	if _, ok := tc.Get("k"); ok {
		t.Fatal("nil cache must miss")
	}
	tc.Close()
}

func TestDiskStatsAndPurge(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDisk(dir, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.Set("layer/1/2/3", []byte("abcd"))
	d.Set("layer/1/2/4", []byte("ef"))
	d.Get("layer/1/2/3") // hit
	d.Get("layer/9/9/9") // miss

	s := d.Stats()
	if s.Files != 2 || s.Bytes != 6 {
		t.Errorf("files/bytes = %d/%d, want 2/6", s.Files, s.Bytes)
	}
	if s.Hits != 1 || s.Misses != 1 {
		t.Errorf("hits/misses = %d/%d, want 1/1", s.Hits, s.Misses)
	}

	if n := d.Purge(); n != 2 {
		t.Errorf("Purge = %d, want 2", n)
	}
	if got := d.Stats().Files; got != 0 {
		t.Errorf("files after purge = %d, want 0", got)
	}
	// The directory itself must survive so later writes need no mkdir.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("cache dir removed by purge: %v", err)
	}
	d.Set("layer/1/2/3", []byte("z"))
	if _, ok := d.Get("layer/1/2/3"); !ok {
		t.Error("cache unusable after purge")
	}
}

func TestTieredStatsDistinguishesOffFromEmpty(t *testing.T) {
	// Memory-only: the disk block must be absent, not a zeroed struct —
	// an all-zero DiskStats would read as "disk cache on but empty".
	tc, err := NewTiered(config.Cache{Enabled: true, MaxEntries: 4, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	s := tc.Stats()
	if !s.Enabled || s.Memory == nil {
		t.Errorf("memory tier missing: %+v", s)
	}
	if s.Disk != nil {
		t.Errorf("disk tier is off, must be omitted: %+v", s.Disk)
	}

	// Caching disabled entirely.
	var off *Tiered
	if off.Stats().Enabled {
		t.Error("nil cache must report Enabled=false")
	}
	if m, d := off.Purge(); m != 0 || d != 0 {
		t.Errorf("nil purge = %d/%d, want 0/0", m, d)
	}
}
