package webui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeCacheCleanupBoundary(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "config.json")
	os.WriteFile(data, []byte("keep"), 0o644)
	for _, name := range []string{"cef-cache", "cef-cache-106"} {
		path := filepath.Join(dir, name, "nested")
		os.MkdirAll(path, 0o755)
		os.WriteFile(filepath.Join(path, "cache"), []byte("delete"), 0o644)
		if err := clearRuntimeCache(dir, name); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("cache remains")
		}
	}
	// Extension webview storage survives the cleanup.
	kept := filepath.Join(dir, "cef-cache", "webview", "ext", "Cookies")
	os.MkdirAll(filepath.Dir(kept), 0o755)
	os.WriteFile(kept, []byte("keep"), 0o644)
	for _, name := range []string{"Cache", "Code Cache", "GPUCache", "DawnCache", "Local Storage", "IndexedDB"} {
		path := filepath.Join(filepath.Dir(kept), name, "entry")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("data"), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "cef-cache", "Cookies"), []byte("delete"), 0o644)
	if err := clearRuntimeCache(dir, "cef-cache"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "cef-cache")); len(entries) != 1 {
		t.Fatal("interface cache remains")
	}
	if b, _ := os.ReadFile(kept); string(b) != "keep" {
		t.Fatal("webview storage was deleted")
	}
	for _, name := range []string{"Cache", "Code Cache", "GPUCache", "DawnCache"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(kept), name)); !os.IsNotExist(err) {
			t.Fatalf("webview cache remains: %s", name)
		}
	}
	for _, name := range []string{"Local Storage", "IndexedDB"} {
		if b, _ := os.ReadFile(filepath.Join(filepath.Dir(kept), name, "entry")); string(b) != "data" {
			t.Fatalf("webview storage was deleted: %s", name)
		}
	}
	if err := clearRuntimeCache(dir, ".."); err == nil {
		t.Fatal("unchecked path accepted")
	}
	if b, _ := os.ReadFile(data); string(b) != "keep" {
		t.Fatal("application data was deleted")
	}
}
