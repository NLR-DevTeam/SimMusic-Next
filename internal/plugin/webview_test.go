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

type fakeWebviews struct {
	mu       sync.Mutex
	opened   []WebviewOptions
	commands []string
	sinks    map[string]func(string, []string)
}

func (f *fakeWebviews) OpenWebview(p *Plugin, o WebviewOptions, sink func(string, []string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, o)
	id := fmt.Sprintf("view%d", len(f.opened))
	f.sinks[id] = sink
	return id, nil
}
func (f *fakeWebviews) WebviewCommand(id, name string, args ...string) error {
	f.mu.Lock()
	f.commands = append(f.commands, id+" "+name+" "+strings.Join(args, "|"))
	sink := f.sinks[id]
	f.mu.Unlock()
	switch name {
	case "exec":
		go sink("result", []string{`{"id":"` + args[0] + `","value":{"answer":42}}`})
	case "close":
		go sink("close", nil)
	}
	return nil
}
func (f *fakeWebviews) ResolveWebviewURL(p *Plugin, relative string) (string, error) {
	return "http://ext.localhost:1/" + relative, nil
}
func (f *fakeWebviews) DisplayWebviewURL(p *Plugin, actual string) string {
	if rest, ok := strings.CutPrefix(actual, "http://ext.localhost:1/"); ok {
		return "ext://" + rest
	}
	return actual
}
func (f *fakeWebviews) WebviewCookies(p *Plugin, url string) ([]WebviewCookie, error) {
	return []WebviewCookie{{Name: "sid", Value: url, Domain: "example.com", Path: "/", HTTPOnly: true}}, nil
}
func (f *fakeWebviews) SetWebviewCookie(p *Plugin, c WebviewCookie) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, "cookie "+c.URL+" "+c.Name+"="+c.Value)
	return nil
}
func (f *fakeWebviews) RemoveWebviewCookies(p *Plugin, url, name string) (int, error) { return 3, nil }
func (f *fakeWebviews) CloseWebviews(pluginID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, "closeAll "+pluginID)
}

const webviewScript = `
const seen = [];
let view;
exports.open = (kind, options) => { view = new sim.webview[kind](options); ["load", "navigation", "close", "message", "error", "title"].forEach(name => view["on" + name] = event => seen.push([name, event.url || event.title || "", event.data === undefined ? null : event.data, event.origin || ""])); view.on("message", event => seen.push(["listener", event.type])); return { id: view.id, kind: view.kind, url: view.url, hasShow: typeof view.show }; };
exports.seen = () => seen;
exports.state = () => ({ url: view.url, title: view.title, closed: view.closed });
exports.post = data => view.postMessage(data);
exports.run = code => view.executeJavaScript(code);
exports.load = url => view.loadURL(url);
exports.close = () => view.close();
exports.cookies = async () => ({ got: await sim.webview.cookies.get("https://example.com/"), set: await view.cookies.set({ url: "https://example.com/", name: "a", value: "b" }), removed: await sim.webview.cookies.clear() });
`

func webviewFixture(t *testing.T, permissions []string) (*Manager, *Plugin, *fakeWebviews) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "extensions"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	manifest, _ := json.Marshal(Manifest{ID: "test", Permissions: permissions})
	if err := m.add(fstest.MapFS{"plugin.json": {Data: manifest}, "main.js": {Data: []byte(webviewScript)}}, "", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	fake := &fakeWebviews{sinks: map[string]func(string, []string){}}
	return m, m.Get("test"), fake
}

func TestWebviewRequiresPermissionAndBackend(t *testing.T) {
	m, p, fake := webviewFixture(t, nil)
	m.SetWebviewBackend(fake)
	if _, err := p.Call(time.Second, "open", "window", map[string]any{"url": "ext://index.html"}); err == nil || !strings.Contains(err.Error(), "webview") {
		t.Fatalf("undeclared permission accepted: %v", err)
	}
	m, p, fake = webviewFixture(t, []string{PermWebview})
	if _, err := p.Call(time.Second, "open", "window", map[string]any{"url": "ext://index.html"}); err == nil || !strings.Contains(err.Error(), "尚未就绪") {
		t.Fatalf("missing backend accepted: %v", err)
	}
	m.SetWebviewBackend(fake)
	// A remote page is network access; pages outside the extension directory are refused.
	for _, address := range []string{"https://example.com/", "ext://../secret.html", "file:///C:/Windows/win.ini", "C:\\Windows\\win.ini", "ext://"} {
		if _, err := p.Call(time.Second, "open", "panel", map[string]any{"url": address}); err == nil {
			t.Fatalf("%s accepted", address)
		}
	}
	if len(fake.opened) != 0 {
		t.Fatal("a refused webview reached the host")
	}
}

func TestWebviewLifecycle(t *testing.T) {
	m, p, fake := webviewFixture(t, []string{PermWebview, PermNetwork})
	m.SetWebviewBackend(fake)
	info, err := p.Call(time.Second, "open", "window", map[string]any{"url": "ext://pages/editor.html?x=1", "title": "编辑", "width": 460, "frame": false, "top": true, "x": 10, "y": 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := info.(map[string]any); got["id"] != "view1" || got["kind"] != "window" || got["url"] != "ext://pages/editor.html?x=1" || got["hasShow"] != "function" {
		t.Fatalf("handle: %v", got)
	}
	o := fake.opened[0]
	if o.Kind != "window" || o.URL != "http://ext.localhost:1/pages/editor.html?x=1" || o.Title != "编辑" || o.Width != 460 || o.Frame || !o.Top || !o.Resizable || !o.Show || !o.Positioned || o.X != 10 || o.Y != 20 {
		t.Fatalf("options: %+v", o)
	}
	sink := fake.sinks["view1"]
	sink("start", []string{"http://ext.localhost:1/pages/editor.html?x=1"})
	sink("load", []string{"http://ext.localhost:1/pages/editor.html?x=1", "200"})
	sink("title", []string{"页面标题"})
	sink("navigation", []string{"https://example.com/next"})
	sink("message", []string{"http://ext.localhost:1/pages/editor.html", `{"type":"ready","n":[1,2]}`})
	sink("message", []string{"https://example.com/next", `"remote"`})
	if _, err := p.Call(time.Second, "post", map[string]any{"type": "load"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Call(time.Second, "load", "https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	value, err := p.Call(time.Second, "run", "return 6 * 7")
	if err != nil || fmt.Sprint(value) != "map[answer:42]" {
		t.Fatalf("executeJavaScript: %v %v", value, err)
	}
	cookies, err := p.Call(time.Second, "cookies")
	if err != nil || !strings.Contains(fmt.Sprint(cookies), "httpOnly:true") || !strings.Contains(fmt.Sprint(cookies), "removed:3") {
		t.Fatalf("cookies: %v %v", cookies, err)
	}
	if _, err := p.Call(time.Second, "close"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		state, _ := p.Call(time.Second, "state")
		if state.(map[string]any)["closed"] == true {
			if got := state.(map[string]any); got["url"] != "https://example.com/next" || got["title"] != "页面标题" {
				t.Fatalf("state: %v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close event not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	seen, _ := p.Call(time.Second, "seen")
	text, _ := json.Marshal(seen)
	want := `[["load","ext://pages/editor.html?x=1",null,""],["title","页面标题",null,""],["navigation","https://example.com/next",null,""],["message","ext://pages/editor.html",{"n":[1,2],"type":"ready"},"ext://"],["listener","message"],["message","https://example.com/next","remote","https://example.com"],["listener","message"],["close","",null,""]]`
	if string(text) != want {
		t.Fatalf("events:\n%s\nwant\n%s", text, want)
	}
	commands := strings.Join(fake.commands, "\n")
	for _, part := range []string{`view1 post {"type":"load"}`, "view1 load https://example.com/a", "view1 exec ", "cookie https://example.com/ a=b", "view1 close"} {
		if !strings.Contains(commands, part) {
			t.Fatalf("missing command %q in\n%s", part, commands)
		}
	}
	if _, err := p.Call(time.Second, "post", 1); err == nil {
		t.Fatal("a closed webview accepted a message")
	}
	// Stopping the extension closes whatever it left open.
	m.StopAll()
	time.Sleep(50 * time.Millisecond)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(strings.Join(fake.commands, "\n"), "closeAll test") {
		t.Fatal("webviews outlive the extension")
	}
}
