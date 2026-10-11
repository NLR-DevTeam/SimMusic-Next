package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"simmusic/internal/config"
	"testing"
	"testing/fstest"
	"time"
)

func TestGojaFileBridge(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "plugins"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	manifest, _ := json.Marshal(Manifest{ID: "file"})
	script := `exports.inspect = async () => {
  const file = new File(["hello", new Uint8Array([32, 119, 111, 114, 108, 100])], "text.txt", {type:"text/plain"});
  const copy = new File([file], "copy.txt");
  const path = sim.path.join(sim.plugin.dataDir, "local.txt"); sim.fs.writeText(path, "disk");
  const local = sim.fs.file(path);
  return {instance: file instanceof File && local instanceof File, text: await copy.text(), bytes: Array.from(new Uint8Array(await local.arrayBuffer())), json: JSON.stringify(file)};
 };`
	if err := manager.AddBuiltin(fstest.MapFS{"plugin.json": {Data: manifest}, "main.js": {Data: []byte(script)}}); err != nil {
		t.Fatal(err)
	}
	defer manager.StopAll()
	var value struct {
		Instance bool
		Text     string
		Bytes    []byte
		JSON     string
	}
	if err := manager.Get("file").CallInto(time.Second, &value, "inspect"); err != nil {
		t.Fatal(err)
	}
	if !value.Instance || value.Text != "hello world" || string(value.Bytes) != "disk" {
		t.Fatalf("File bridge: %+v", value)
	}
	var serialized map[string]any
	if err := json.Unmarshal([]byte(value.JSON), &serialized); err != nil || serialized["text"] != nil || serialized["arrayBuffer"] != nil {
		t.Fatalf("methods leaked into resource JSON: %s", value.JSON)
	}
}

func TestBuiltinReloadReadsEditableFiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "plugins", "local")
	os.MkdirAll(source, 0o755)
	os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"local","version":"1"}`), 0o644)
	os.WriteFile(filepath.Join(source, "main.js"), []byte(`exports.value = () => "before";`), 0o644)
	manager := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "plugins"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	if err := manager.AddBuiltinDir(source); err != nil {
		t.Fatal(err)
	}
	defer manager.StopAll()
	manager.LoadUserPlugins()
	if len(manager.All()) != 1 {
		t.Fatal("on-disk builtin loaded twice")
	}
	os.WriteFile(filepath.Join(source, "main.js"), []byte(`exports.value = () => "after";`), 0o644)
	os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"id":"local","version":"2"}`), 0o644)
	if err := manager.Reload("local"); err != nil {
		t.Fatal(err)
	}
	value, err := manager.Get("local").Call(time.Second, "value")
	if err != nil || value != "after" || manager.Get("local").Manifest.Version != "2" {
		t.Fatalf("reload ignored editable files: %v %v", value, err)
	}
}
