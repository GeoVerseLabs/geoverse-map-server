package cache

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// keySegRe matches path segments that are safe to map onto the filesystem
// (layer names are validated at config time; tile coords are integers).
var keySegRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Disk is a persistent tile cache backed by plain files. Entries expire
// after ttl (checked via mtime) and a background janitor keeps the total
// size under maxBytes by deleting the oldest files first.
type Disk struct {
	dir      string
	ttl      time.Duration
	maxBytes int64

	stopOnce sync.Once
	stop     chan struct{}

	hits   atomic.Uint64
	misses atomic.Uint64
}

// DiskStats is a point-in-time snapshot of the disk tier. Files/Bytes are
// measured by walking the cache directory, so callers should treat Stats
// as an operator-facing diagnostic rather than a hot-path metric.
type DiskStats struct {
	Dir        string  `json:"dir"`
	Files      int     `json:"files"`
	Bytes      int64   `json:"bytes"`
	MaxBytes   int64   `json:"maxBytes"`
	Hits       uint64  `json:"hits"`
	Misses     uint64  `json:"misses"`
	HitRate    float64 `json:"hitRate"`
	TTLSeconds float64 `json:"ttlSeconds"`
}

// NewDisk creates the cache directory (if needed) and starts the janitor.
// maxSizeMB <= 0 disables the size bound; ttl <= 0 disables expiry.
func NewDisk(dir string, ttl time.Duration, maxSizeMB int64) (*Disk, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	d := &Disk{
		dir:      dir,
		ttl:      ttl,
		maxBytes: maxSizeMB * 1024 * 1024,
		stop:     make(chan struct{}),
	}
	go d.janitor()
	return d, nil
}

// path maps a cache key like "layer/6/52/24" to a file below dir.
// Returns "" for keys containing unsafe segments.
func (d *Disk) path(key string) string {
	segs := strings.Split(key, "/")
	for _, s := range segs {
		// The regex admits dots, so "." and ".." must be rejected
		// explicitly to prevent path traversal.
		if s == "." || s == ".." || !keySegRe.MatchString(s) {
			return ""
		}
	}
	return filepath.Join(append([]string{d.dir}, segs...)...)
}

// Get returns the cached value for key if present and fresh.
func (d *Disk) Get(key string) ([]byte, bool) {
	if d == nil {
		return nil, false
	}
	p := d.path(key)
	if p == "" {
		d.misses.Add(1)
		return nil, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		d.misses.Add(1)
		return nil, false
	}
	if d.ttl > 0 && time.Since(fi.ModTime()) > d.ttl {
		_ = os.Remove(p)
		d.misses.Add(1)
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		d.misses.Add(1)
		return nil, false
	}
	d.hits.Add(1)
	return data, true
}

// Set stores value under key, writing atomically (temp file + rename) so
// concurrent readers never observe partial tiles.
func (d *Disk) Set(key string, value []byte) {
	if d == nil {
		return
	}
	p := d.path(key)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, p); err != nil {
		_ = os.Remove(name)
	}
}

// Purge deletes every cached file, returning how many were removed. The
// cache directory itself is kept so that subsequent writes need no mkdir.
func (d *Disk) Purge() int {
	if d == nil {
		return 0
	}
	n := 0
	_ = filepath.WalkDir(d.dir, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		if os.Remove(path) == nil {
			n++
		}
		return nil
	})
	return n
}

// Stats snapshots the disk tier, walking the cache directory to size it.
func (d *Disk) Stats() DiskStats {
	if d == nil {
		return DiskStats{}
	}
	s := DiskStats{
		Dir:        d.dir,
		MaxBytes:   d.maxBytes,
		Hits:       d.hits.Load(),
		Misses:     d.misses.Load(),
		TTLSeconds: d.ttl.Seconds(),
	}
	_ = filepath.WalkDir(d.dir, func(_ string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		fi, err := de.Info()
		if err != nil {
			return nil
		}
		s.Files++
		s.Bytes += fi.Size()
		return nil
	})
	if total := s.Hits + s.Misses; total > 0 {
		s.HitRate = float64(s.Hits) / float64(total)
	}
	return s
}

// Close stops the janitor goroutine.
func (d *Disk) Close() {
	if d == nil {
		return
	}
	d.stopOnce.Do(func() { close(d.stop) })
}

func (d *Disk) janitor() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.Sweep()
		}
	}
}

type diskEntry struct {
	path  string
	size  int64
	mtime time.Time
}

// Sweep removes expired entries and, if the cache exceeds its size bound,
// deletes the oldest entries until usage drops to 90% of the bound.
// It is exported for tests and can be called at any time.
func (d *Disk) Sweep() {
	var entries []diskEntry
	var total int64
	now := time.Now()
	_ = filepath.WalkDir(d.dir, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		fi, err := de.Info()
		if err != nil {
			return nil
		}
		// Orphaned temp files from crashed writes.
		if strings.HasPrefix(de.Name(), ".tmp-") && now.Sub(fi.ModTime()) > time.Hour {
			_ = os.Remove(path)
			return nil
		}
		if d.ttl > 0 && now.Sub(fi.ModTime()) > d.ttl {
			_ = os.Remove(path)
			return nil
		}
		entries = append(entries, diskEntry{path: path, size: fi.Size(), mtime: fi.ModTime()})
		total += fi.Size()
		return nil
	})
	if d.maxBytes <= 0 || total <= d.maxBytes {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mtime.Before(entries[j].mtime) })
	target := d.maxBytes * 9 / 10
	for _, e := range entries {
		if total <= target {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}
