package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// WebviewOptions describes a page an extension shows in its own browser, apart from the host page.
type WebviewOptions struct {
	Kind       string // "panel"：主窗口内容区；"window"：独立窗口
	URL        string
	Title      string
	Icon       string
	Background string
	Width      int
	Height     int
	MinWidth   int
	MinHeight  int
	MaxWidth   int
	MaxHeight  int
	X, Y       int
	Positioned bool
	// 独立窗口的外观与行为
	Resizable, Frame, Top, Fullscreen, Show, SkipTaskbar, Minimizable, Maximizable bool
}

// WebviewCookie mirrors what sim.webview.cookies exchanges with extensions.
type WebviewCookie struct {
	URL      string  `json:"url,omitempty"`
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	Expires  float64 `json:"expires,omitempty"` // Unix 秒；0 为会话 Cookie
}

// WebviewBackend is the browser host. Events reach sink in order, from a goroutine of the backend:
// start(url) load(url, status) fail(url, code, text) navigation(url) title(text)
// message(url, json) result(json) close().
type WebviewBackend interface {
	OpenWebview(p *Plugin, o WebviewOptions, sink func(event string, args []string)) (string, error)
	// WebviewCommand: close, load, reload, post, exec, show, hide, focus, minimize, maximize,
	// restore, title, top, fullscreen, size, position, center.
	WebviewCommand(id, name string, args ...string) error
	// ResolveWebviewURL maps ext:// and relative addresses onto the extension's own origin.
	ResolveWebviewURL(p *Plugin, relative string) (string, error)
	// DisplayWebviewURL is the inverse, for addresses reported back to the extension.
	DisplayWebviewURL(p *Plugin, actual string) string
	WebviewCookies(p *Plugin, url string) ([]WebviewCookie, error)
	SetWebviewCookie(p *Plugin, c WebviewCookie) error
	RemoveWebviewCookies(p *Plugin, url, name string) (int, error)
	CloseWebviews(pluginID string)
}

// SetWebviewBackend connects the browser host once the interface exists.
func (m *Manager) SetWebviewBackend(b WebviewBackend) {
	m.mu.Lock()
	m.webviews = b
	m.mu.Unlock()
}

func (m *Manager) webviewBackend() WebviewBackend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.webviews
}

// Source is the file system holding the extension's own files.
func (p *Plugin) Source() fs.FS { return p.src }

// webviewURL validates an address an extension wants shown and returns what the browser loads.
func (p *Plugin) webviewURL(b WebviewBackend, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "about:blank" {
		return "about:blank"
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		p.need(PermNetwork)
		p.checkHost(raw)
		return raw
	}
	relative := raw
	if strings.HasPrefix(lower, "ext://") {
		relative = raw[len("ext://"):]
	} else if u, err := url.Parse(raw); err != nil || u.Scheme != "" {
		p.throw(errors.New("webview 只能加载 http(s) 地址或扩展目录内的页面（ext://）"))
	}
	file := relative
	if at := strings.IndexAny(file, "?#"); at >= 0 {
		file = file[:at]
	}
	if file = path.Clean("/" + strings.ReplaceAll(file, "\\", "/")); strings.Contains(relative, "..") || file == "/" {
		p.throw(errors.New("无效的扩展页面地址：" + raw))
	}
	resolved, err := b.ResolveWebviewURL(p, strings.TrimLeft(strings.ReplaceAll(relative, "\\", "/"), "/"))
	if err != nil {
		p.throw(err)
	}
	return resolved
}

func optionInt(o map[string]any, key string) (int, bool) {
	switch v := o[key].(type) {
	case int64:
		return int(v), true
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return int(v), true
		}
	}
	return 0, false
}

func optionBool(o map[string]any, key string, def bool) bool {
	if v, ok := o[key].(bool); ok {
		return v
	}
	return def
}

var webviewEvents = []string{"load", "navigation", "close", "message", "error", "title"}

// webview is the extension-side handle of one browser.
type webview struct {
	p       *Plugin
	rs      *runState
	b       WebviewBackend
	id      string
	kind    string
	obj     *goja.Object
	mu      sync.Mutex
	closed  bool
	url     string
	title   string
	pending map[string]func(value any, err error)
	// listeners added through on(); the on<event> properties are read from obj when an event fires.
	listeners map[string][]goja.Value
}

func (w *webview) command(name string, args ...string) {
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		w.p.throw(errors.New("webview 已关闭"))
	}
	if err := w.b.WebviewCommand(w.id, name, args...); err != nil {
		w.p.throw(err)
	}
}

// emit runs the handlers of one event on the extension's own thread.
func (w *webview) emit(kind string, fields map[string]any) {
	rt := w.p.rt
	event := rt.NewObject()
	event.Set("type", kind)
	event.Set("target", w.obj)
	for key, value := range fields {
		event.Set(key, value)
	}
	handlers := append([]goja.Value{w.obj.Get("on" + kind)}, w.listeners[kind]...)
	for _, handler := range handlers {
		if fn, ok := goja.AssertFunction(handler); ok {
			if _, err := fn(w.obj, event); err != nil {
				w.p.logf("webview %s 事件处理出错: %v", kind, err)
			}
		}
	}
}

func (w *webview) settle(id string, value any, err error) {
	w.mu.Lock()
	done := w.pending[id]
	delete(w.pending, id)
	w.mu.Unlock()
	if done != nil {
		done(value, err)
	}
}

// sink receives the backend's events; everything that touches JS is posted to the extension's loop.
func (w *webview) sink(event string, args []string) {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	shown := func(actual string) string { return w.b.DisplayWebviewURL(w.p, actual) }
	w.p.postState(w.rs, func() {
		switch event {
		case "start":
			w.url = shown(arg(0))
		case "load":
			w.url = shown(arg(0))
			var status int
			fmt.Sscan(arg(1), &status)
			w.emit("load", map[string]any{"url": w.url, "status": status})
		case "fail":
			var code int
			fmt.Sscan(arg(1), &code)
			w.emit("error", map[string]any{"url": shown(arg(0)), "code": code, "message": arg(2)})
		case "navigation":
			w.url = shown(arg(0))
			w.emit("navigation", map[string]any{"url": w.url})
		case "title":
			w.title = arg(0)
			w.emit("title", map[string]any{"title": w.title})
		case "message":
			parse, _ := goja.AssertFunction(w.p.rt.Get("JSON").ToObject(w.p.rt).Get("parse"))
			data, err := parse(goja.Undefined(), w.p.rt.ToValue(arg(1)))
			if err != nil {
				return
			}
			address := shown(arg(0))
			origin := "ext://"
			if u, err := url.Parse(address); err == nil && u.Scheme != "ext" {
				origin = u.Scheme + "://" + u.Host
			}
			w.emit("message", map[string]any{"data": data, "origin": origin, "url": address})
		case "result":
			var reply struct {
				ID    string `json:"id"`
				Value any    `json:"value"`
				Error string `json:"error"`
			}
			if json.Unmarshal([]byte(arg(0)), &reply) != nil {
				return
			}
			if reply.Error != "" {
				w.settle(reply.ID, nil, errors.New(reply.Error))
			} else {
				w.settle(reply.ID, reply.Value, nil)
			}
		case "close":
			w.mu.Lock()
			already := w.closed
			w.closed = true
			pending := w.pending
			w.pending = map[string]func(any, error){}
			w.mu.Unlock()
			for _, done := range pending {
				done(nil, errors.New("webview 已关闭"))
			}
			if !already {
				w.emit("close", nil)
			}
		}
		// Promise reactions queued by the handlers run when the VM is left.
		_, _ = w.p.rt.RunString("void 0")
	})
}

func (p *Plugin) newWebview(rs *runState, kind string, call goja.ConstructorCall) *goja.Object {
	p.need(PermWebview)
	b := p.mgr.webviewBackend()
	if b == nil {
		p.throw(errors.New("界面尚未就绪，暂时不能创建 webview"))
	}
	rt := p.rt
	o, _ := call.Argument(0).Export().(map[string]any)
	if o == nil {
		o = map[string]any{}
	}
	if s, ok := call.Argument(0).Export().(string); ok {
		o["url"] = s
	}
	text := func(key string) string { s, _ := o[key].(string); return s }
	number := func(key string) int { v, _ := optionInt(o, key); return v }
	opts := WebviewOptions{Kind: kind, URL: p.webviewURL(b, text("url")), Title: text("title"), Background: text("backgroundColor"),
		Width: number("width"), Height: number("height"), MinWidth: number("minWidth"), MinHeight: number("minHeight"),
		MaxWidth: number("maxWidth"), MaxHeight: number("maxHeight"),
		Resizable: optionBool(o, "resizable", true), Frame: optionBool(o, "frame", true), Top: optionBool(o, "top", false),
		Fullscreen: optionBool(o, "fullscreen", false), Show: optionBool(o, "show", true), SkipTaskbar: optionBool(o, "skipTaskbar", false),
		Minimizable: optionBool(o, "minimizable", true), Maximizable: optionBool(o, "maximizable", true)}
	x, hasX := optionInt(o, "x")
	y, hasY := optionInt(o, "y")
	opts.X, opts.Y, opts.Positioned = x, y, hasX && hasY
	if icon := text("icon"); icon != "" {
		// The extension's own files need no permission; anything else is an ordinary file read.
		if strings.HasPrefix(strings.ToLower(icon), "ext://") {
			icon = icon[len("ext://"):]
		}
		if !filepath.IsAbs(icon) && p.Dir != "" {
			icon = filepath.Join(p.Dir, filepath.FromSlash(icon))
			if rel, err := filepath.Rel(p.Dir, icon); err != nil || strings.HasPrefix(rel, "..") {
				p.throw(errors.New("无效的图标路径"))
			}
		} else {
			icon = p.checkPath(icon)
		}
		opts.Icon = icon
	}
	obj := call.This
	w := &webview{p: p, rs: rs, b: b, kind: kind, obj: obj, title: opts.Title, pending: map[string]func(any, error){}, listeners: map[string][]goja.Value{}}
	w.url = b.DisplayWebviewURL(p, opts.URL)
	id, err := b.OpenWebview(p, opts, w.sink)
	if err != nil {
		p.throw(err)
	}
	w.id = id
	obj.Set("id", id)
	obj.Set("kind", kind)
	for _, name := range webviewEvents {
		obj.Set("on"+name, goja.Null())
	}
	obj.DefineAccessorProperty("closed", rt.ToValue(func() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.closed }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	obj.DefineAccessorProperty("url", rt.ToValue(func() string { return w.url }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	obj.DefineAccessorProperty("title", rt.ToValue(func() string { return w.title }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	known := func(name string) string {
		for _, event := range webviewEvents {
			if event == name {
				return name
			}
		}
		p.throw(errors.New("未知的 webview 事件：" + name))
		return ""
	}
	obj.Set("on", func(name string, handler goja.Value) *goja.Object {
		if _, ok := goja.AssertFunction(handler); ok {
			w.listeners[known(name)] = append(w.listeners[name], handler)
		}
		return obj
	})
	obj.Set("off", func(name string, handler goja.Value) *goja.Object {
		list := w.listeners[known(name)]
		for i, item := range list {
			if item.SameAs(handler) {
				w.listeners[name] = append(append([]goja.Value(nil), list[:i]...), list[i+1:]...)
				break
			}
		}
		return obj
	})
	obj.Set("close", func() {
		w.mu.Lock()
		closed := w.closed
		w.mu.Unlock()
		if !closed {
			_ = b.WebviewCommand(id, "close")
		}
	})
	obj.Set("loadURL", func(address string) { w.command("load", p.webviewURL(b, address)) })
	obj.Set("reload", func() { w.command("reload") })
	obj.Set("postMessage", func(data goja.Value) {
		text := "null"
		if data != nil && !goja.IsUndefined(data) {
			stringify, _ := goja.AssertFunction(rt.Get("JSON").ToObject(rt).Get("stringify"))
			v, err := stringify(goja.Undefined(), data)
			if err != nil {
				panic(err)
			}
			if !goja.IsUndefined(v) {
				text = v.String()
			}
		}
		w.command("post", text)
	})
	// The code is the body of an async function in the page: `return` gives the result.
	obj.Set("executeJavaScript", func(code string) goja.Value {
		promise, resolve, reject := rt.NewPromise()
		var token [8]byte
		rand.Read(token[:])
		key := hex.EncodeToString(token[:])
		finish := func(value any, err error) {
			if err != nil {
				reject(rt.NewGoError(err))
			} else {
				resolve(p.toJS(value))
			}
		}
		w.mu.Lock()
		closed := w.closed
		if !closed {
			w.pending[key] = finish
		}
		w.mu.Unlock()
		if closed {
			reject(rt.NewGoError(errors.New("webview 已关闭")))
			return rt.ToValue(promise)
		}
		if err := b.WebviewCommand(id, "exec", key, code); err != nil {
			w.settle(key, nil, err)
			return rt.ToValue(promise)
		}
		time.AfterFunc(30*time.Second, func() {
			p.postState(rs, func() {
				w.settle(key, nil, errors.New("页面脚本执行超时"))
				_, _ = rt.RunString("void 0")
			})
		})
		return rt.ToValue(promise)
	})
	obj.Set("cookies", rt.Get("sim").ToObject(rt).Get("webview").ToObject(rt).Get("cookies"))
	if kind == "window" {
		flag := func(v bool) string {
			if v {
				return "1"
			}
			return "0"
		}
		for _, name := range []string{"show", "hide", "focus", "minimize", "maximize", "restore", "center"} {
			obj.Set(name, func() { w.command(name) })
		}
		obj.Set("setTitle", func(title string) { w.title = title; w.command("title", title) })
		obj.Set("setTop", func(on bool) { w.command("top", flag(on)) })
		obj.Set("setFullscreen", func(on bool) { w.command("fullscreen", flag(on)) })
		obj.Set("setSize", func(width, height int) { w.command("size", fmt.Sprint(width), fmt.Sprint(height)) })
		obj.Set("setPosition", func(x, y int) { w.command("position", fmt.Sprint(x), fmt.Sprint(y)) })
	}
	return nil
}

func (p *Plugin) installWebview(rs *runState, sim *goja.Object) {
	rt := p.rt
	wv := rt.NewObject()
	sim.Set("webview", wv)
	backend := func() WebviewBackend {
		p.need(PermWebview)
		b := p.mgr.webviewBackend()
		if b == nil {
			p.throw(errors.New("界面尚未就绪，暂时不能使用 webview"))
		}
		return b
	}
	// Cookies belong to the extension's own browser storage, shared by all of its webviews.
	cookies := rt.NewObject()
	wv.Set("cookies", cookies)
	cookies.Set("get", func(address string) goja.Value {
		b := backend()
		return p.async(func() (any, error) {
			list, err := b.WebviewCookies(p, address)
			if list == nil {
				list = []WebviewCookie{}
			}
			return list, err
		})
	})
	cookies.Set("set", func(c WebviewCookie) goja.Value {
		b := backend()
		if c.URL == "" || c.Name == "" {
			p.throw(errors.New("设置 Cookie 需要 url 与 name"))
		}
		return p.async(func() (any, error) { return true, b.SetWebviewCookie(p, c) })
	})
	cookies.Set("remove", func(address, name string) goja.Value {
		b := backend()
		if address == "" {
			p.throw(errors.New("删除 Cookie 需要 url；清空全部请使用 clear()"))
		}
		return p.async(func() (any, error) { return b.RemoveWebviewCookies(p, address, name) })
	})
	cookies.Set("clear", func() goja.Value {
		b := backend()
		return p.async(func() (any, error) { return b.RemoveWebviewCookies(p, "", "") })
	})
	wv.Set("panel", func(call goja.ConstructorCall) *goja.Object { return p.newWebview(rs, "panel", call) })
	wv.Set("window", func(call goja.ConstructorCall) *goja.Object { return p.newWebview(rs, "window", call) })
	wv.Set("offscreen", func(call goja.ConstructorCall) *goja.Object { return p.newWebview(rs, "offscreen", call) })
	go func() {
		<-rs.quit
		if b := p.mgr.webviewBackend(); b != nil {
			b.CloseWebviews(p.ID())
		}
	}()
}
