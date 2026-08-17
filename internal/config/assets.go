package config

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const mebibyte = int64(1024 * 1024)
const maxAssetSizeMB = math.MaxInt64 / mebibyte

// Assets defines the safety policy for file-backed data sources.
//
// Root is an allowlist boundary, not a new base directory: source.path keeps
// its historical working-directory-relative meaning. Omitting Root or leaving
// EnforceRoot false preserves legacy path behaviour; doctor reports that as a
// compatibility-mode warning.
type Assets struct {
	Root                string `yaml:"root" json:"root,omitempty"`
	EnforceRoot         bool   `yaml:"enforce_root" json:"enforce_root"`
	MaxFileSizeMB       int64  `yaml:"max_file_size_mb" json:"max_file_size_mb,omitempty"`
	MaxMemoryFileSizeMB int64  `yaml:"max_memory_file_size_mb" json:"max_memory_file_size_mb,omitempty"`
}

// AssetInfo is the inspected identity of a file-backed source.
type AssetInfo struct {
	RequestedPath string    `json:"requestedPath"`
	ResolvedPath  string    `json:"resolvedPath"`
	Size          int64     `json:"size"`
	ModTime       time.Time `json:"modTime"`
	fileInfo      os.FileInfo
}

// Validate checks the structural policy without touching the filesystem.
func (a Assets) Validate() error {
	if a.EnforceRoot && strings.TrimSpace(a.Root) == "" {
		return fmt.Errorf("assets.enforce_root is true but assets.root is empty")
	}
	if a.MaxFileSizeMB < 0 {
		return fmt.Errorf("assets.max_file_size_mb must be >= 0")
	}
	if a.MaxMemoryFileSizeMB < 0 {
		return fmt.Errorf("assets.max_memory_file_size_mb must be >= 0")
	}
	if a.MaxFileSizeMB > maxAssetSizeMB {
		return fmt.Errorf("assets.max_file_size_mb must be <= %d", maxAssetSizeMB)
	}
	if a.MaxMemoryFileSizeMB > maxAssetSizeMB {
		return fmt.Errorf("assets.max_memory_file_size_mb must be <= %d", maxAssetSizeMB)
	}
	return nil
}

// InspectAsset resolves path, rejects non-regular files, applies the root
// boundary and enforces the configured size limit. memoryBacked selects the
// tighter limit used by sources that materialize the whole dataset in memory.
func (a Assets) InspectAsset(path string, memoryBacked bool) (AssetInfo, error) {
	requested := strings.TrimSpace(path)
	if requested == "" {
		return AssetInfo{}, fmt.Errorf("asset path is empty")
	}
	abs, err := filepath.Abs(requested)
	if err != nil {
		return AssetInfo{}, fmt.Errorf("resolve asset path %q: %w", requested, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return AssetInfo{}, fmt.Errorf("resolve asset path %q: %w", requested, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return AssetInfo{}, fmt.Errorf("normalize asset path %q: %w", requested, err)
	}
	if a.EnforceRoot {
		root, err := canonicalRoot(a.Root)
		if err != nil {
			return AssetInfo{}, err
		}
		if !withinRoot(root, resolved) {
			return AssetInfo{}, fmt.Errorf("asset path %q resolves outside assets.root", requested)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return AssetInfo{}, fmt.Errorf("stat asset %q: %w", requested, err)
	}
	if !info.Mode().IsRegular() {
		return AssetInfo{}, fmt.Errorf("asset path %q is not a regular file", requested)
	}
	limit := a.limitBytes(memoryBacked)
	if limit > 0 && info.Size() > limit {
		return AssetInfo{}, fmt.Errorf("asset path %q is %d bytes, exceeds limit %d bytes", requested, info.Size(), limit)
	}
	return AssetInfo{
		RequestedPath: requested,
		ResolvedPath:  filepath.Clean(resolved),
		Size:          info.Size(),
		ModTime:       info.ModTime(),
		fileInfo:      info,
	}, nil
}

// OpenAsset performs a second identity check after opening. The check prevents
// a symlink or file swap between canonicalization and the first read from
// turning a previously accepted archive into an out-of-root file.
func (a Assets) OpenAsset(path string, memoryBacked bool) (*os.File, AssetInfo, error) {
	before, err := a.InspectAsset(path, memoryBacked)
	if err != nil {
		return nil, AssetInfo{}, err
	}
	f, err := os.Open(before.ResolvedPath)
	if err != nil {
		return nil, AssetInfo{}, fmt.Errorf("open asset %q: %w", before.RequestedPath, err)
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, AssetInfo{}, fmt.Errorf("stat opened asset %q: %w", before.RequestedPath, err)
	}
	after, err := a.InspectAsset(before.ResolvedPath, memoryBacked)
	if err != nil {
		f.Close()
		return nil, AssetInfo{}, err
	}
	if before.ResolvedPath != after.ResolvedPath || !os.SameFile(opened, after.fileInfo) {
		f.Close()
		return nil, AssetInfo{}, fmt.Errorf("asset path %q changed while it was being opened", before.RequestedPath)
	}
	return f, after, nil
}

// ReadAsset reads a file-backed in-memory source while retaining the size
// guard even if the file grows after its initial stat.
func (a Assets) ReadAsset(path string, memoryBacked bool) ([]byte, AssetInfo, error) {
	f, info, err := a.OpenAsset(path, memoryBacked)
	if err != nil {
		return nil, AssetInfo{}, err
	}
	defer f.Close()
	limit := a.limitBytes(memoryBacked)
	var r io.Reader = f
	if limit > 0 {
		r = io.LimitReader(f, limit+1)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, AssetInfo{}, fmt.Errorf("read asset %q: %w", info.RequestedPath, err)
	}
	if limit > 0 && int64(len(raw)) > limit {
		return nil, AssetInfo{}, fmt.Errorf("asset path %q grew beyond limit %d bytes while reading", info.RequestedPath, limit)
	}
	return raw, info, nil
}

func (a Assets) limitBytes(memoryBacked bool) int64 {
	limitMB := a.MaxFileSizeMB
	if memoryBacked && a.MaxMemoryFileSizeMB > 0 && (limitMB == 0 || a.MaxMemoryFileSizeMB < limitMB) {
		limitMB = a.MaxMemoryFileSizeMB
	}
	if limitMB <= 0 {
		return 0
	}
	return limitMB * mebibyte
}

func canonicalRoot(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("resolve assets.root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve assets.root %q: %w", path, err)
	}
	return filepath.Abs(resolved)
}

func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
