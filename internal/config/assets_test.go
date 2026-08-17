package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeAsset(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAssetsInspectWithinRoot(t *testing.T) {
	root := t.TempDir()
	path := writeAsset(t, root, "inside.pmtiles", 32)
	policy := Assets{Root: root, EnforceRoot: true, MaxFileSizeMB: 1}
	info, err := policy.InspectAsset(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 32 || !filepath.IsAbs(info.ResolvedPath) {
		t.Fatalf("unexpected asset info: %+v", info)
	}
}

func TestAssetsRejectOutsideRootAndDirectory(t *testing.T) {
	root := t.TempDir()
	outside := writeAsset(t, t.TempDir(), "outside.pmtiles", 1)
	policy := Assets{Root: root, EnforceRoot: true}
	if _, err := policy.InspectAsset(outside, false); err == nil || !strings.Contains(err.Error(), "outside assets.root") {
		t.Fatalf("outside root error = %v", err)
	}
	if _, err := policy.InspectAsset(root, false); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory error = %v", err)
	}
}

func TestAssetsRejectSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outDir := t.TempDir()
	out := writeAsset(t, outDir, "outside.pmtiles", 1)
	link := filepath.Join(root, "escape.pmtiles")
	if err := os.Symlink(out, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable on this Windows host: %v", err)
		}
		t.Fatal(err)
	}
	policy := Assets{Root: root, EnforceRoot: true}
	if _, err := policy.InspectAsset(link, false); err == nil || !strings.Contains(err.Error(), "outside assets.root") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestAssetsApplySeparateMemoryLimit(t *testing.T) {
	root := t.TempDir()
	path := writeAsset(t, root, "large.geojson", 2*mebibyte)
	policy := Assets{
		Root: root, EnforceRoot: true,
		MaxFileSizeMB: 4, MaxMemoryFileSizeMB: 1,
	}
	if _, err := policy.InspectAsset(path, false); err != nil {
		t.Fatalf("archive-sized policy should allow file: %v", err)
	}
	if _, err := policy.InspectAsset(path, true); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("memory limit error = %v", err)
	}
}

func TestAssetsValidation(t *testing.T) {
	for _, tc := range []Assets{
		{EnforceRoot: true},
		{MaxFileSizeMB: -1},
		{MaxMemoryFileSizeMB: -1},
		{MaxFileSizeMB: maxAssetSizeMB + 1},
		{MaxMemoryFileSizeMB: maxAssetSizeMB + 1},
	} {
		if err := tc.Validate(); err == nil {
			t.Fatalf("expected invalid policy: %+v", tc)
		}
	}
}
