package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/config"
)

// fakeAdmin stands in for the host's asking: it allows or refuses, and carries out what it allowed.
type fakeAdmin struct {
	m     *Manager
	allow bool
	asked []string
}

func (f *fakeAdmin) PluginSetEnabled(by *Plugin, id string, on bool) (bool, error) {
	f.asked = append(f.asked, fmt.Sprint("enable ", id, " ", on))
	if !f.allow {
		return false, nil
	}
	return true, f.m.Keeping(by, func() error { return f.m.SetEnabled(id, on) })
}
func (f *fakeAdmin) PluginUninstall(by *Plugin, id string) (bool, error) {
	f.asked = append(f.asked, "uninstall "+id)
	return f.allow, nil
}
func (f *fakeAdmin) PluginInstall(by *Plugin, path string) (bool, error) {
	f.asked = append(f.asked, "install "+filepath.Base(path))
	return f.allow, nil
}
func (f *fakeAdmin) PluginReload(by *Plugin, id string) error { return nil }

const storeScript = `
let runs = sim.settings.get("runs", 0) + 1;
sim.settings.set("runs", runs);
exports.list = () => sim.plugins.list().map(item => item.id + ":" + item.enabled + ":" + item.running);
exports.get = id => sim.plugins.get(id);
exports.disable = id => sim.plugins.setEnabled(id, false);
exports.others = async () => [await sim.plugins.uninstall("other"), await sim.plugins.install(sim.path.join(sim.plugin.dataDir, "pack.simex"))];
`

func manageFixture(t *testing.T, permissions []string) (*Manager, *fakeAdmin) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "extensions"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	admin := &fakeAdmin{m: m}
	m.SetPluginAdmin(admin)
	store, _ := json.Marshal(Manifest{ID: "store", Name: "商店", Permissions: permissions})
	other, _ := json.Marshal(Manifest{ID: "other", Name: "另一个"})
	for _, files := range []fstest.MapFS{
		{"plugin.json": {Data: store}, "main.js": {Data: []byte(storeScript)}},
		{"plugin.json": {Data: other}, "main.js": {Data: []byte("exports.ping = () => 1;")}},
	} {
		if err := m.add(files, "", true); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(m.StopAll)
	return m, admin
}

func TestPluginManagementRequiresPermission(t *testing.T) {
	m, admin := manageFixture(t, nil)
	admin.allow = true
	for _, name := range []string{"list", "get", "disable", "others"} {
		if _, err := m.Get("store").Call(time.Second, name, "other"); err == nil || !strings.Contains(err.Error(), "plugin") {
			t.Fatalf("%s without the permission: %v", name, err)
		}
	}
	if len(admin.asked) != 0 || !m.Get("other").Running() {
		t.Fatal("an extension without the permission reached the host")
	}
}

func TestPluginManagement(t *testing.T) {
	m, admin := manageFixture(t, []string{PermPlugins})
	store := m.Get("store")
	if list, err := store.Call(time.Second, "list"); err != nil || fmt.Sprint(list) != "[store:true:true other:true:true]" {
		t.Fatalf("list: %v %v", list, err)
	}
	if missing, err := store.Call(time.Second, "get", "nobody"); err != nil || missing != nil {
		t.Fatalf("an extension that is not installed: %v %v", missing, err)
	}
	// Every request is put to the user; a refusal changes nothing and is not an error.
	if done, err := store.Call(time.Second, "disable", "other"); err != nil || done != false || !m.Get("other").Running() {
		t.Fatalf("a refused request: %v %v", done, err)
	}
	if answers, err := store.Call(time.Second, "others"); err != nil || fmt.Sprint(answers) != "[false false]" {
		t.Fatalf("refused removal and installation: %v %v", answers, err)
	}
	if got := strings.Join(admin.asked, ", "); got != "enable other false, uninstall other, install pack.simex" {
		t.Fatalf("asked: %s", got)
	}
	// Allowed: the other extension stops, and the one that asked is not restarted by its own request.
	admin.allow = true
	if done, err := store.Call(5*time.Second, "disable", "other"); err != nil || done != true {
		t.Fatalf("an allowed request: %v %v", done, err)
	}
	if m.Get("other").Running() || !store.Running() || store.Settings.Get("runs") != float64(1) && store.Settings.Get("runs") != int64(1) {
		t.Fatalf("after disabling: other running %v, store running %v, store started %v times", m.Get("other").Running(), store.Running(), store.Settings.Get("runs"))
	}
}
