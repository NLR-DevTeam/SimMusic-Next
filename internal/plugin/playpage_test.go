package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/config"
)

type fakePlayer struct {
	mu       sync.Mutex
	commands []string
}

func (f *fakePlayer) Toast(string)                                 {}
func (f *fakePlayer) Alert(string)                                 {}
func (f *fakePlayer) RefreshMenus()                                {}
func (f *fakePlayer) PickFolder(string) (string, error)            { return "", nil }
func (f *fakePlayer) PickFiles(string, []string) ([]string, error) { return nil, nil }
func (f *fakePlayer) PlayerState() map[string]any {
	return map[string]any{"playing": true, "queue": []any{map[string]any{"id": "1"}}}
}
func (f *fakePlayer) PlayerCommand(name string, args []any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, fmt.Sprint(name, args))
	return true, nil
}

const playerScript = `
exports.state = () => sim.player.state();
exports.control = async () => { await sim.player.seek(12.5); await sim.player.moveInQueue(2, 0); return sim.player.toggle(); };
exports.post = () => sim.playPage.postMessage({ hello: 1 });
`

func playerFixture(t *testing.T, permissions []string) (*Plugin, *fakePlayer) {
	t.Helper()
	dir := t.TempDir()
	host := &fakePlayer{}
	m := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "extensions"), config.Open(filepath.Join(dir, "state.json"), nil), host)
	manifest, _ := json.Marshal(Manifest{ID: "test", Permissions: permissions, PlayPages: []PlayPage{{ID: "main", Name: "页面", URL: "page/index.html?a=1"}}})
	if err := m.add(fstest.MapFS{"plugin.json": {Data: manifest}, "main.js": {Data: []byte(playerScript)}}, "", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	return m.Get("test"), host
}

func TestPlayerRequiresPermission(t *testing.T) {
	p, host := playerFixture(t, []string{PermPlayPage, PermWebview})
	for _, name := range []string{"state", "control", "post"} {
		if _, err := p.Call(time.Second, name); err == nil || !strings.Contains(err.Error(), "player") {
			t.Fatalf("%s without the permission: %v", name, err)
		}
	}
	if len(host.commands) != 0 || p.PlayPagesAllowed() {
		t.Fatal("playback control reached the host without the permission")
	}
}

func TestPlayerControl(t *testing.T) {
	p, host := playerFixture(t, []string{PermPlayer, PermPlayPage})
	state, err := p.Call(time.Second, "state")
	if err != nil || state.(map[string]any)["playing"] != true {
		t.Fatalf("state: %v %v", state, err)
	}
	if _, err := p.Call(time.Second, "control"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(host.commands, " "); got != "seek[12.5] moveInQueue[2 0] toggle[]" {
		t.Fatalf("commands: %s", got)
	}
	if !p.PlayPagesAllowed() {
		t.Fatal("granted play pages are not offered")
	}
	// Revoking either permission withdraws the pages.
	p.mgr.SetGranted("test", PermPlayPage, false)
	if p.PlayPagesAllowed() {
		t.Fatal("play pages offered without the play page permission")
	}
	// No play page is loaded: the message has nowhere to go.
	if sent, err := p.Call(time.Second, "post"); err != nil || sent != false {
		t.Fatalf("post: %v %v", sent, err)
	}
}

func TestPlayPageDeclarations(t *testing.T) {
	valid := []PlayPage{{ID: "a", Name: "甲", URL: "pages/a.html#x", Preview: "pages/a.png", Background: "#10aBcD"}, {ID: "b", Name: "乙", URL: "b.html"}}
	if err := validatePlayPages(valid); err != nil {
		t.Fatal(err)
	}
	for name, pages := range map[string][]PlayPage{
		"duplicate id":  {{ID: "a", Name: "甲", URL: "a.html"}, {ID: "a", Name: "乙", URL: "b.html"}},
		"unsafe id":     {{ID: "../a", Name: "甲", URL: "a.html"}},
		"no name":       {{ID: "a", URL: "a.html"}},
		"remote page":   {{ID: "a", Name: "甲", URL: "https://example.com/"}},
		"escaping page": {{ID: "a", Name: "甲", URL: "../a.html"}},
		"absolute":      {{ID: "a", Name: "甲", URL: "/a.html"}},
		"preview":       {{ID: "a", Name: "甲", URL: "a.html", Preview: "C:\\a.png"}},
		"colour":        {{ID: "a", Name: "甲", URL: "a.html", Background: "red"}},
	} {
		if validatePlayPages(pages) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
