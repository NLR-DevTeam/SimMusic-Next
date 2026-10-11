package plugin

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"simmusic/internal/config"
	"strings"
	"testing"
	"time"
)

func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, text := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		entry.Write([]byte(text))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSimexAndDevelopmentLifecycle(t *testing.T) {
	root := t.TempDir()
	state := config.Open(filepath.Join(root, "state.json"), nil)
	manager := NewManager(filepath.Join(root, "data"), filepath.Join(root, "plugins"), state, nil)
	data := zipFixture(t, map[string]string{"plugin.json": `{"id":"fixture","main":"main.js"}`, "main.js": `exports.value=()=>"package";`})
	staged, _, err := PreparePackage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staged)
	installed, err := manager.Install(staged)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Development || installed.Dir != filepath.Join(root, "plugins", "fixture") {
		t.Fatal("package must be managed")
	}
	source := filepath.Join(root, "source")
	os.MkdirAll(source, 0755)
	os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"fixture","main":"main.js"}`), 0644)
	os.WriteFile(filepath.Join(source, "main.js"), []byte(`exports.value=()=>"development";`), 0644)
	developed, err := manager.LoadDevelopment(source)
	if err != nil {
		t.Fatal(err)
	}
	if !developed.Development || developed.Dir != source {
		t.Fatal("source must be referenced")
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "fixture")); !os.IsNotExist(err) {
		t.Fatal("development must not be copied")
	}
	os.WriteFile(filepath.Join(source, "main.js"), []byte(`exports.value=()=>"edited";`), 0644)
	if err := manager.Reload("fixture"); err != nil {
		t.Fatal(err)
	}
	value, err := manager.Get("fixture").Call(time.Second, "value")
	if err != nil || value != "edited" {
		t.Fatalf("reload: %v %v", value, err)
	}
	manager.StopAll()
	if err := state.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened := NewManager(filepath.Join(root, "data"), filepath.Join(root, "plugins"), config.Open(filepath.Join(root, "state.json"), nil), nil)
	reopened.LoadUserPlugins()
	defer reopened.StopAll()
	if p := reopened.Get("fixture"); p == nil || !p.Development || p.Dir != source {
		t.Fatal("development reference did not persist")
	}
	if err := reopened.Remove("fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "main.js")); err != nil {
		t.Fatal("removal deleted the developer's code")
	}
}

func TestSimexRejectsBadArchives(t *testing.T) {
	for _, name := range []string{"../escape.js", `C:\escape.js`, "/escape.js", "folder/../../escape.js"} {
		t.Run(name, func(t *testing.T) {
			data := zipFixture(t, map[string]string{"plugin.json": `{"id":"fixture"}`, "main.js": "", name: "bad"})
			dir, _, err := PreparePackage(bytes.NewReader(data))
			if err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if dir != "" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("rejected archive left extracted code")
				}
			}
		})
	}
	for _, id := range []string{"../outside", "CON", "NUL.txt", "foo/../bar"} {
		data := zipFixture(t, map[string]string{"plugin.json": fmt.Sprintf(`{"id":%q}`, id), "main.js": ""})
		if _, _, err := PreparePackage(bytes.NewReader(data)); err == nil {
			t.Fatalf("unsafe id accepted: %s", id)
		}
	}
	if _, _, err := PreparePackage(strings.NewReader("not a zip")); err == nil {
		t.Fatal("invalid zip accepted")
	}
}
