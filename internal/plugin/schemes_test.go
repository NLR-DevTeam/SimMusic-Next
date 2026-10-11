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

func addSchemeFixture(t *testing.T, m *Manager, man Manifest, script string) *Plugin {
	t.Helper()
	data, _ := json.Marshal(man)
	if err := m.AddBuiltin(fstest.MapFS{"plugin.json": {Data: data}, "main.js": {Data: []byte(script)}}); err != nil {
		t.Fatal(err)
	}
	return m.Get(man.ID)
}

func TestSchemeRegistrationOrderAndPermission(t *testing.T) {
	dir := t.TempDir()
	state := config.Open(filepath.Join(dir, "state.json"), nil)
	m := NewManager(dir, filepath.Join(dir, "plugins"), state, nil)
	t.Cleanup(m.StopAll)
	owner := addSchemeFixture(t, m, Manifest{ID: "owner", Version: "1", Schemes: []SchemeDeclaration{{Scheme: "alpha", Handlers: map[string]string{"getMusicInfo": "alphaInfo"}}, {Scheme: "beta", Handlers: map[string]string{"getMusicInfo": "betaInfo"}}}}, `exports.alphaInfo = (ids,scene,scheme) => ({source:"alpha",scheme}); exports.betaInfo = () => ({source:"beta"});`)
	if owner.LoadErr() != nil || m.Scheme("alpha") == nil || m.Scheme("beta") == nil {
		t.Fatal("multiple schemes did not register")
	}
	duplicate := addSchemeFixture(t, m, Manifest{ID: "duplicate", Schemes: []SchemeDeclaration{{Scheme: "partial"}, {Scheme: "alpha"}}}, ``)
	if duplicate.Running() || duplicate.LoadErr() == nil || m.Scheme("partial") != nil || !strings.Contains(strings.Join(duplicate.Logs(), "\n"), "alpha") {
		t.Fatal("duplicate registration must fail transactionally and log")
	}
	patchMan := Manifest{ID: "patch", Permissions: []string{PermSchemeExtend}, SchemeExtensions: []SchemeDeclaration{{Scheme: "alpha", Handlers: map[string]string{"getMusicInfo": "patchedInfo"}}}}
	m.SetGranted("patch", PermSchemeExtend, false)
	patch := addSchemeFixture(t, m, patchMan, `exports.patchedInfo = (ids,scene,scheme) => ({source:"patch",scheme});`)
	provider, _ := m.Scheme("alpha").Handler("getMusicInfo")
	if provider != owner || !patch.Running() {
		t.Fatal("ungranted patch must not replace owner")
	}
	m.SetGranted("patch", PermSchemeExtend, true)
	m.RestartOrdered()
	provider, _ = m.Scheme("alpha").Handler("getMusicInfo")
	if provider != patch {
		t.Fatal("granted patch was not applied")
	}
	var result struct{ Source, Scheme string }
	if err := m.Scheme("alpha").CallInto(time.Second, &result, "getMusicInfo", []string{"1"}, "play"); err != nil || result.Source != "patch" || result.Scheme != "alpha" {
		t.Fatalf("named handler dispatch: %+v %v", result, err)
	}
	before := m.Scheme("alpha").Version()
	m.SetGranted("patch", PermSchemeExtend, false)
	provider, _ = m.Scheme("alpha").Handler("getMusicInfo")
	if provider != owner || before == m.Scheme("alpha").Version() {
		t.Fatal("revocation must immediately reject the override and invalidate cache version")
	}
	m.SetGranted("patch", PermSchemeExtend, true)
	if err := m.SetOrder([]string{"patch", "owner", "duplicate"}); err != nil {
		t.Fatal(err)
	}
	if patch.Running() || patch.LoadErr() == nil || !strings.Contains(patch.LoadErr().Error(), "不存在") {
		t.Fatal("patch cannot precede its target")
	}
	if err := m.SetOrder([]string{"owner", "patch", "duplicate"}); err != nil || !patch.Running() {
		t.Fatal("restoring order should load patch")
	}
	var saved []string
	if !config.Open(filepath.Join(dir, "state.json"), nil).Unmarshal("plugin.loadOrder", &saved) || saved[0] != "owner" {
		t.Fatal("order did not persist")
	}
	if err := m.SetOrder([]string{"owner", "owner", "patch"}); err == nil {
		t.Fatal("invalid order accepted")
	}
	bad := addSchemeFixture(t, m, Manifest{ID: "missing", Permissions: []string{PermSchemeExtend}, SchemeExtensions: []SchemeDeclaration{{Scheme: "absent"}}}, ``)
	if bad.Running() || bad.LoadErr() == nil {
		t.Fatal("nonexistent target must fail")
	}
}

func TestReloadCancelsOldVMJobsAndInterruptsTimeout(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, filepath.Join(dir, "plugins"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	t.Cleanup(m.StopAll)
	p := addSchemeFixture(t, m, Manifest{ID: "timers"}, `const generation = (sim.settings.get("generation") || 0) + 1; sim.settings.set("generation", generation); setInterval(() => {if (sim.settings.get("generation") !== generation) sim.settings.set("stale",true);},10); exports.pending = () => new Promise(resolve => setTimeout(() => resolve(generation),300)); exports.generation = () => generation; exports.busy = () => {while(true){}};`)
	pending := make(chan error, 1)
	go func() { _, err := p.Call(time.Second, "pending"); pending <- err }()
	time.Sleep(20 * time.Millisecond)
	m.RestartOrdered()
	select {
	case err := <-pending:
		if err == nil {
			t.Fatal("old pending call survived reload")
		}
	case <-time.After(time.Second):
		t.Fatal("reload left caller waiting on dead VM")
	}
	time.Sleep(320 * time.Millisecond)
	if p.Settings.Bool("stale") {
		t.Fatal("old timer entered replacement runtime")
	}
	var generation int
	if err := p.CallInto(time.Second, &generation, "generation"); err != nil || generation != 2 {
		t.Fatal("replacement runtime did not load", generation, err)
	}
	start := time.Now()
	if _, err := p.Call(30*time.Millisecond, "busy"); err == nil || p.Running() || m.Scheme("timers") != nil || time.Since(start) > time.Second {
		t.Fatal("timed-out JS was not interrupted/unregistered", err)
	}
	m.RestartOrdered()
	if !p.Running() {
		t.Fatal("timed-out extension could not reload")
	}
}
