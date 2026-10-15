package plugin

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/config"
)

func permissionFixture(t *testing.T, permissions []string, builtin bool, values map[string]any) (*Manager, *Plugin, *config.Store) {
	t.Helper()
	dir := t.TempDir()
	store := config.Open(filepath.Join(dir, "state.json"), nil)
	for key, value := range values {
		store.Set(key, value)
	}
	m := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "extensions"), store, nil)
	manifest, _ := json.Marshal(Manifest{ID: "test", Permissions: permissions})
	source := fstest.MapFS{
		"plugin.json": {Data: manifest},
		"main.js":     {Data: []byte(`exports.getSidebar = () => ({ title: "Test", items: [] }); exports.onSidebarOpen = () => ({ tracks: [] }); exports.permission = () => sim.permissions.has("sidebar"); exports.refresh = () => sim.ui.refresh();`)},
	}
	if err := m.add(source, "", builtin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m, m.Get("test"), store
}

func TestSidebarRequiresDeclarationAndGrant(t *testing.T) {
	m, p, _ := permissionFixture(t, nil, true, nil)
	m.SetGranted(p.ID(), PermSidebar, true)
	if p.HasPermission(PermSidebar) {
		t.Fatal("grant must not bypass a missing manifest declaration")
	}
	if _, err := p.Call(time.Second, "getSidebar"); err == nil || !strings.Contains(err.Error(), "未在 plugin.json 中声明") {
		t.Fatalf("undeclared sidebar callback: %v", err)
	}
	if _, err := p.Call(time.Second, "refresh"); err == nil {
		t.Fatal("undeclared sim.ui.refresh must fail inside Goja")
	}
}

func TestSidebarInstallGrantAndRevocation(t *testing.T) {
	m, p, store := permissionFixture(t, []string{PermSidebar}, false, nil)
	if p.HasPermission(PermSidebar) {
		t.Fatal("third-party declarations must not grant themselves")
	}
	m.GrantAll(p.Manifest)
	if value, err := p.Call(time.Second, "permission"); err != nil || value != true {
		t.Fatalf("Goja must see the install grant: %v, %v", value, err)
	}
	if _, err := p.Call(time.Second, "getSidebar"); err != nil {
		t.Fatal(err)
	}
	m.SetGranted(p.ID(), PermSidebar, false)
	if value, err := p.Call(time.Second, "permission"); err != nil || value != false {
		t.Fatalf("Goja must see immediate revocation: %v, %v", value, err)
	}
	for _, method := range []string{"getSidebar", "onSidebarAdd", "onSidebarOpen", "getSidebarMenu", "onSidebarMenu", "refresh"} {
		if _, err := p.Call(time.Second, method); err == nil || !strings.Contains(err.Error(), "已撤销") {
			t.Fatalf("revoked %s: %v", method, err)
		}
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened := config.Open(filepath.Join(filepath.Dir(m.baseData), "state.json"), nil)
	if value, ok := reopened.Get(grantKey(p.ID(), PermSidebar)).(bool); !ok || value {
		t.Fatal("revocation must persist")
	}
}
