package webui

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"simmusic/internal/config"
	"simmusic/internal/core"
	"simmusic/internal/plugin"
)

// Packages dropped together are handed out one at a time, in order, each prepared for its own installer;
// a package that cannot be read is skipped with a fixed message, and the next drop replaces what is left.
func TestDroppedPackagesQueue(t *testing.T) {
	dir := t.TempDir()
	a := &core.App{DataDir: dir, State: config.Open(filepath.Join(dir, "state.json"), nil)}
	a.Plugins = plugin.NewManager(filepath.Join(dir, "plugin-data"), filepath.Join(dir, "plugins"), a.State, a)
	u := New(a, nil)
	t.Cleanup(u.clearInstall)
	pack := func(id string) string {
		path := filepath.Join(dir, id+".simex")
		file, _ := os.Create(path)
		archive := zip.NewWriter(file)
		manifest, _ := archive.Create("plugin.json")
		manifest.Write([]byte(`{"id":"` + id + `","name":"` + id + `","version":"1","main":"main.js"}`))
		main, _ := archive.Create("main.js")
		main.Write([]byte("module.exports = {};"))
		archive.Close()
		file.Close()
		return path
	}
	first, second, missing := pack("first"), pack("second"), filepath.Join(dir, "gone.simex")
	os.WriteFile(missing, nil, 0o644)
	drop := func(paths ...string) any {
		u.mu.Lock()
		u.dragPaths = paths
		u.mu.Unlock()
		result, err := u.call(request{Action: "dropFiles", Text: "home"})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)["packages"]
	}
	next := func() (string, error) {
		result, err := u.call(request{Action: "nextDroppedPackage"})
		if result == nil || err != nil {
			return "", err
		}
		return result.(map[string]any)["id"].(string), nil
	}

	if count := drop(first, missing, second); count != 3 {
		t.Fatalf("packages %v, want 3", count)
	}
	if id, err := next(); id != "first" || err != nil {
		t.Fatalf("first package %q, %v", id, err)
	}
	os.Remove(missing)
	if _, err := next(); err == nil || err.Error() != "无法读取扩展包" {
		t.Fatalf("unreadable package: %v", err)
	}
	if id, err := next(); id != "second" || err != nil {
		t.Fatalf("second package %q, %v", id, err)
	}
	if id, err := next(); id != "" || err != nil {
		t.Fatalf("after the last package %q, %v", id, err)
	}

	drop(first, second)
	next()
	drop(second)
	if id, _ := next(); id != "second" {
		t.Fatalf("a new drop replaces the waiting packages, got %q", id)
	}
	if id, _ := next(); id != "" {
		t.Fatalf("left over %q", id)
	}
}
