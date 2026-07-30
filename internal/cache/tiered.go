package cache

import (
	"github.com/GeoVerseLabs/geoverse-map-server/internal/config"
)

// Tiered combines the in-memory LRU (first tier) with the optional disk
// cache (second tier). Either tier may be absent; a nil *Tiered is a
// valid no-op cache.
type Tiered struct {
	mem  *Cache
	disk *Disk
}

// NewTiered builds the cache stack described by cfg. It returns nil when
// caching is disabled entirely.
func NewTiered(cfg config.Cache) (*Tiered, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	t := &Tiered{}
	if cfg.MaxEntries > 0 {
		t.mem = New(cfg.MaxEntries, cfg.TTL)
	}
	if cfg.Disk.Enabled {
		d, err := NewDisk(cfg.Disk.Dir, cfg.Disk.TTL, cfg.Disk.MaxSizeMB)
		if err != nil {
			return nil, err
		}
		t.disk = d
	}
	return t, nil
}

// Get checks memory first, then disk. Disk hits are promoted to memory.
func (t *Tiered) Get(key string) ([]byte, bool) {
	if t == nil {
		return nil, false
	}
	if v, ok := t.mem.Get(key); ok {
		return v, true
	}
	if v, ok := t.disk.Get(key); ok {
		t.mem.Set(key, v)
		return v, true
	}
	return nil, false
}

// Set writes to both tiers.
func (t *Tiered) Set(key string, value []byte) {
	if t == nil {
		return
	}
	t.mem.Set(key, value)
	t.disk.Set(key, value)
}

// Stats is a snapshot of both tiers. Memory/Disk are nil when that tier
// is not configured, which lets callers distinguish "tier is off" from
// "tier is on but empty" — an all-zero struct cannot.
type Stats struct {
	Enabled bool       `json:"enabled"`
	Memory  *MemStats  `json:"memory,omitempty"`
	Disk    *DiskStats `json:"disk,omitempty"`
}

// Stats snapshots the whole cache stack.
func (t *Tiered) Stats() Stats {
	if t == nil {
		return Stats{}
	}
	s := Stats{Enabled: true}
	if t.mem != nil {
		m := t.mem.Stats()
		s.Memory = &m
	}
	if t.disk != nil {
		d := t.disk.Stats()
		s.Disk = &d
	}
	return s
}

// Purge empties both tiers, returning the number of entries dropped from
// each. Purging is idempotent and safe to call on a live server: tiles are
// regenerated on the next request.
func (t *Tiered) Purge() (mem, disk int) {
	if t == nil {
		return 0, 0
	}
	return t.mem.Purge(), t.disk.Purge()
}

// Close releases background resources (the disk janitor).
func (t *Tiered) Close() {
	if t == nil {
		return
	}
	t.disk.Close()
}
