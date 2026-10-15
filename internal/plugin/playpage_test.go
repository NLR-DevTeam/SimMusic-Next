package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/config"
	"simmusic/internal/model"
)

type fakePlayer struct {
	mu       sync.Mutex
	commands []string
	paused   bool
	volume   float64
	file     string
}

func (f *fakePlayer) Toast(string)                                 {}
func (f *fakePlayer) Alert(string)                                 {}
func (f *fakePlayer) RefreshMenus()                                {}
func (f *fakePlayer) PickFolder(string) (string, error)            { return "", nil }
func (f *fakePlayer) PickFiles(string, []string) ([]string, error) { return nil, nil }
func (f *fakePlayer) PlayerState() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"playing": !f.paused, "loading": false, "position": 3.5, "duration": 60.0, "speed": 1.0, "volume": f.volume,
		"current": map[string]any{"id": "1"}, "index": 0, "queue": []any{map[string]any{"id": "1"}}}
}
func (f *fakePlayer) PlayerCover(source, id string, large bool) []byte {
	if id == "none" {
		return nil
	}
	return []byte(pngHeader + source + id + fmt.Sprint(large))
}
func (f *fakePlayer) PlayerSpectrum(bands int) []float64 { return make([]float64, bands) }
func (f *fakePlayer) PlayerQueue(p *Plugin, op string, tracks []model.Track, options QueueOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, fmt.Sprintf("%s %s:%s %d %v %v", op, tracks[0].Source, tracks[0].ID, options.Index, *options.Position, *options.Play))
	return nil
}
func (f *fakePlayer) PlayerPlayback(source, id string) (model.Resource, error) {
	if id == "file" {
		return model.Resource{Path: f.file}, nil
	}
	return model.Resource{URL: "https://cdn.example.com/" + id, Headers: map[string]string{"Referer": "https://example.com/"}}, nil
}

func (f *fakePlayer) PlayerLoadInfo(indexes []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, fmt.Sprint("loadInfo", indexes))
}
func (f *fakePlayer) PlayerCommand(name string, args []any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, fmt.Sprint(name, args))
	if name == "pause" {
		f.paused = true
	}
	if name == "setVolume" {
		f.volume, _ = args[0].(float64)
	}
	return true, nil
}

const pngHeader = "\x89PNG\r\n\x1a\n"

const playerScript = `
exports.state = () => sim.player.state;
exports.events = () => new Promise(resolve => {
    const seen = [];
    const state = sim.player.state;
    sim.player.on("status", current => seen.push("status " + current.playing + " " + (current === state)));
    sim.player.on("track", () => seen.push("track"));
    sim.player.on("change", event => { seen.push("change " + event.changed.join(",")); if (!event.state.playing) resolve({ seen, position: sim.player.position(), volume: state.volume }); });
    sim.player.setVolume(.25).then(() => sim.player.pause());
});
exports.cover = async () => {
    const file = await sim.player.cover({ source: "s", id: "7" }, { large: true });
    const path = sim.path.join(sim.plugin.dataDir, "cover.png");
    sim.fs.writeFile(path, file);
    return { name: file.name, type: file.type, size: file.size, saved: sim.fs.stat(path).size, none: await sim.player.cover({ source: "s", id: "none" }), load: await sim.player.loadInfo([1, 2]) };
};
exports.control = async () => { await sim.player.seek(12.5); await sim.player.moveInQueue(2, 0); return sim.player.toggle(); };
exports.post = () => sim.playPage.postMessage({ hello: 1 });
exports.queue = async () => {
    await sim.player.setQueue([{ source: "s", id: "9" }], { index: 0, position: 30, play: false });
    const url = await sim.player.playback({ source: "s", id: "9" });
    const file = await sim.player.playback({ source: "s", id: "file" });
    return { url, file: { name: file.name, size: file.size, path: file.path, isFile: file instanceof File, expire: "expire" in url } };
};
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
	for _, name := range []string{"state", "control", "post", "events", "cover", "queue"} {
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
	// What changes in the player arrives as events, on the one state object, without the script asking.
	events, err := p.Call(5*time.Second, "events")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(events); !strings.Contains(string(got), `"status false true"`) || strings.Contains(string(got), `"track"`) || !strings.Contains(string(got), `"position":3.5`) || !strings.Contains(string(got), `"volume":0.25`) {
		t.Fatalf("events: %s", got)
	}
	cover, err := p.Call(5*time.Second, "cover")
	if got, _ := json.Marshal(cover); err != nil || string(got) != `{"load":true,"name":"cover.png","none":null,"saved":14,"size":14,"type":"image/png"}` {
		t.Fatalf("cover: %s %v", got, err)
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

func TestPlayerQueueAndPlayback(t *testing.T) {
	p, host := playerFixture(t, []string{PermPlayer})
	host.file = filepath.Join(t.TempDir(), "a.flac")
	if err := os.WriteFile(host.file, []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := p.Call(5*time.Second, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(host.commands, " "); got != "set s:9 0 30 false" {
		t.Fatalf("queue: %s", got)
	}
	// An address comes with its request headers and nothing else; a file as a File of its path.
	got, _ := json.Marshal(result)
	want, _ := json.Marshal(map[string]any{"url": map[string]any{"url": "https://cdn.example.com/9", "headers": map[string]any{"Referer": "https://example.com/"}},
		"file": map[string]any{"name": "a.flac", "size": 4, "path": host.file, "isFile": true, "expire": false}})
	if string(got) != string(want) {
		t.Fatalf("playback: %s", got)
	}
}
