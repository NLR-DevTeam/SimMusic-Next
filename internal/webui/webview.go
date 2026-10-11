package webui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"simmusic/internal/plugin"
)

// Extension webviews are separate browsers of the CEF host: a child of the main window laid over
// the content area (panel) or a top-level window. Commands go to the host as lines on its standard
// input, events come back as "WV:" lines on its output; fields are tab-separated and escaped.

type hostView struct {
	id     string
	kind   string
	plugin *plugin.Plugin
	sink   func(event string, args []string)
	loaded bool
	queue  []string // page scripts waiting for the document to finish loading
}

type extOrigin struct {
	label  string
	plugin *plugin.Plugin
}

type webviewHost struct {
	mu      sync.Mutex
	in      io.Writer
	backlog []string
	views   map[string]*hostView
	panel   string
	origins map[string]*extOrigin // by extension id
	extAddr string                // port of the listener serving extension pages
	waits   map[string]chan []string
	serial  uint64
}

var hostEscaper = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r")
var hostUnescaper = strings.NewReplacer("\\\\", "\\", "\\t", "\t", "\\n", "\n", "\\r", "\r")

func (u *UI) hostSend(fields ...string) {
	for i, field := range fields {
		fields[i] = hostEscaper.Replace(field)
	}
	line := strings.Join(fields, "\t") + "\n"
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.in == nil {
		h.backlog = append(h.backlog, line)
		return
	}
	io.WriteString(h.in, line)
}

// attachHost starts delivering commands once the host process reads its input.
func (u *UI) attachHost(in io.Writer) {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	h.in = in
	for _, line := range h.backlog {
		io.WriteString(in, line)
	}
	h.backlog = nil
}

func (u *UI) view(id string) *hostView {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.views[id]
}

// hostEvent handles one "WV:" line of the host.
func (u *UI) hostEvent(line string) {
	fields := strings.Split(line, "\t")
	for i, field := range fields {
		fields[i] = hostUnescaper.Replace(field)
	}
	if len(fields) < 2 {
		return
	}
	event, id, args := fields[0], fields[1], fields[2:]
	h := &u.webviews
	if strings.HasPrefix(event, "cookie") {
		h.mu.Lock()
		wait := h.waits[id]
		h.mu.Unlock()
		if wait != nil {
			wait <- append([]string{event}, args...)
		}
		return
	}
	h.mu.Lock()
	v := h.views[id]
	var flush []string
	if v != nil {
		switch event {
		case "start":
			v.loaded = false
		case "load":
			v.loaded = true
			flush, v.queue = v.queue, nil
		case "closed":
			delete(h.views, id)
			if h.panel == id {
				h.panel = ""
			}
		}
	}
	h.mu.Unlock()
	if v == nil {
		return
	}
	for _, script := range flush {
		u.hostSend("eval", id, script)
	}
	switch event {
	case "start", "load", "fail", "title":
		v.sink(event, args)
	case "nav":
		v.sink("navigation", args)
	case "msg":
		if len(args) == 3 && args[1] == "m" {
			v.sink("message", []string{args[0], args[2]})
		} else if len(args) == 3 && args[1] == "r" {
			v.sink("result", args[2:])
		}
	case "closed":
		v.sink("close", nil)
	}
}

func flag(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

func (u *UI) OpenWebview(p *plugin.Plugin, o plugin.WebviewOptions, sink func(string, []string)) (string, error) {
	if o.Kind != "panel" && o.Kind != "window" && o.Kind != "offscreen" {
		return "", errors.New("未知的 webview 类型")
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(token[:])
	base, err := u.extBase(p)
	if err != nil {
		return "", err
	}
	h := &u.webviews
	h.mu.Lock()
	if h.views == nil {
		h.views = map[string]*hostView{}
	}
	h.views[id] = &hostView{id: id, kind: o.Kind, plugin: p, sink: sink}
	replaced := ""
	if o.Kind == "panel" {
		// One panel at a time: a new one takes the content area over.
		replaced, h.panel = h.panel, id
	}
	h.mu.Unlock()
	if replaced != "" {
		u.hostSend("close", replaced)
	}
	flags := 0
	for bit, on := range []bool{o.Resizable, o.Frame, o.Top, o.Fullscreen, o.Show, o.SkipTaskbar, o.Minimizable, o.Maximizable, o.Positioned, o.Title != ""} {
		if on {
			flags |= 1 << bit
		}
	}
	n := strconv.Itoa
	u.hostSend("create", id, o.Kind, o.URL, base, filepath.Join(u.app.DataDir, "cef-cache", "webview", p.ID()), o.Title, o.Icon, o.Background,
		n(o.Width), n(o.Height), n(o.MinWidth), n(o.MinHeight), n(o.MaxWidth), n(o.MaxHeight), n(o.X), n(o.Y), n(flags))
	return id, nil
}

func jsString(text string) string {
	b, _ := json.Marshal(text)
	// U+2028/2029 end a line in a script but not in JSON.
	return strings.NewReplacer(" ", "\\u2028", " ", "\\u2029").Replace(string(b))
}

func (u *UI) WebviewCommand(id, name string, args ...string) error {
	v := u.view(id)
	if v == nil {
		return errors.New("webview 已关闭")
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	// Scripts for the page wait until its document has loaded, in the order they were issued.
	script := func(code string) {
		h := &u.webviews
		h.mu.Lock()
		if !v.loaded {
			v.queue = append(v.queue, code)
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		u.hostSend("eval", id, code)
	}
	switch name {
	case "close", "reload":
		u.hostSend(name, id)
	case "load":
		u.hostSend("load", id, arg(0))
	case "post":
		script(`window.__simHostReceive&&window.__simHostReceive("m",` + jsString(arg(0)) + `)`)
	case "exec":
		// A script that does not parse never registers itself; the second one then reports that.
		script(`window.__simHostReceive&&window.__simHostReceive("e",` + jsString(arg(0)) + ",async function(){\n" + arg(1) + "\n})")
		script(`window.__simHostReceive&&window.__simHostReceive("c",` + jsString(arg(0)) + `)`)
	case "show", "hide", "focus", "minimize", "maximize", "restore", "center", "title", "top", "fullscreen", "size", "position":
		if v.kind != "window" {
			return errors.New("此操作只适用于独立窗口")
		}
		u.hostSend("win", id, name, arg(0), arg(1))
	default:
		return errors.New("未知的 webview 操作：" + name)
	}
	return nil
}

func (u *UI) CloseWebviews(pluginID string) {
	h := &u.webviews
	h.mu.Lock()
	var ids []string
	for id, v := range h.views {
		if v.plugin.ID() == pluginID {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		u.hostSend("close", id)
	}
}

// openViews lists the webviews the host has not reported closed, for the log.
func (u *UI) openViews() []string {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, v := range h.views {
		out = append(out, v.kind+":"+v.plugin.ID())
	}
	return out
}

// ---------- 面板 ----------

func (u *UI) activePanel() any {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[h.panel]; v != nil {
		return map[string]string{"id": v.id, "plugin": v.plugin.ID()}
	}
	return nil
}

func (u *UI) panelID() string {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.panel
}

// panelLayout places the panel: insets from the main window's client area in CSS pixels, and
// the rectangles of host elements shown above it (menus, dialogs, notices), cut out of the panel.
func (u *UI) panelLayout(id string, value any) {
	if id == "" || id != u.panelID() {
		return
	}
	layout, _ := value.(map[string]any)
	number := func(v any) string { f, _ := v.(float64); return strconv.Itoa(int(f + .5)) }
	fields := []string{"layout", id, number(layout["left"]), number(layout["top"]), number(layout["right"]), number(layout["bottom"]), flag(layout["visible"] == true)}
	holes, _ := layout["holes"].([]any)
	for _, hole := range holes {
		if rect, _ := hole.([]any); len(rect) == 4 && len(fields) < 7+4*16 {
			fields = append(fields, number(rect[0]), number(rect[1]), number(rect[2]), number(rect[3]))
		}
	}
	u.hostSend(fields...)
}

func (u *UI) panelClose(id string) {
	if id != "" && id == u.panelID() {
		u.hostSend("close", id)
	}
}

// ---------- 扩展页面 ----------

// extBase returns the origin serving an extension's own files: http://<label>.localhost:<port>/.
// Every extension has its own, apart from the host interface and from other extensions.
func (u *UI) extBase(p *plugin.Plugin) (string, error) {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.extAddr == "" {
		return "", errors.New("界面尚未就绪，暂时不能创建 webview")
	}
	if h.origins == nil {
		h.origins = map[string]*extOrigin{}
	}
	o := h.origins[p.ID()]
	if o == nil {
		var token [8]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", err
		}
		o = &extOrigin{label: "ext" + hex.EncodeToString(token[:])}
		h.origins[p.ID()] = o
	}
	// A reloaded extension is a new Plugin value with the files of the new version.
	o.plugin = p
	return "http://" + o.label + ".localhost:" + h.extAddr + "/", nil
}

func (u *UI) ResolveWebviewURL(p *plugin.Plugin, relative string) (string, error) {
	base, err := u.extBase(p)
	return base + relative, err
}

func (u *UI) DisplayWebviewURL(p *plugin.Plugin, actual string) string {
	if base, err := u.extBase(p); err == nil && strings.HasPrefix(actual, base) {
		return "ext://" + strings.TrimPrefix(actual, base)
	}
	return actual
}

// Parts of the host interface extension pages may use to look like it: /.sim/<name>.
var extKit = map[string]string{
	"simui.css": "simui.v5.css", "simui.js": "simui.v5.js", "cssRules.js": "cssRules.js", "runtime.css": "runtime.css", "assets/fonts/remixicon.ttf": "assets/fonts/remixicon.ttf",
}

func (u *UI) startExtServer() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	h := &u.webviews
	h.mu.Lock()
	h.extAddr = port
	h.mu.Unlock()
	web, _ := fs.Sub(files, "web")
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label, _, _ := strings.Cut(r.Host, ".")
		var source fs.FS
		h.mu.Lock()
		for _, o := range h.origins {
			if o.label == label && r.Host == o.label+".localhost:"+port {
				source = o.plugin.Source()
			}
		}
		h.mu.Unlock()
		if source == nil || r.Method != "GET" && r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if name, ok := strings.CutPrefix(r.URL.Path, "/.sim/"); ok {
			if name == "fonts.css" {
				// The icon font, and the interface font the user chose: a family installed on this machine.
				family := strings.Map(func(r rune) rune {
					if r < 0x20 || strings.ContainsRune("\"'\\;{}<>", r) {
						return -1
					}
					return r
				}, u.app.Cfg.String("general.font"))
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				fmt.Fprintf(w, "@font-face { font-family: \"icon\"; src: url(\"assets/fonts/remixicon.ttf\"); }\nhtml:root { font-family: \"%s\", \"Microsoft YaHei\", sans-serif; }\n", family)
				return
			}
			file, known := extKit[name]
			if !known {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "private, max-age=86400")
			http.ServeFileFS(w, r, web, file)
			return
		}
		// Extension files are edited in place during development.
		w.Header().Set("Cache-Control", "no-store")
		http.FileServerFS(source).ServeHTTP(w, r)
	})}
	go server.Serve(ln)
	go func() { <-u.quit; server.Close() }()
	return nil
}

// ---------- Cookie ----------

func (u *UI) cookieRequest(p *plugin.Plugin, command string, args ...string) ([][]string, error) {
	h := &u.webviews
	h.mu.Lock()
	if h.waits == nil {
		h.waits = map[string]chan []string{}
	}
	h.serial++
	request := "c" + strconv.FormatUint(h.serial, 10)
	wait := make(chan []string, 64)
	h.waits[request] = wait
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.waits, request); h.mu.Unlock() }()
	u.hostSend(append([]string{command, request, filepath.Join(u.app.DataDir, "cef-cache", "webview", p.ID())}, args...)...)
	var rows [][]string
	timeout := time.After(15 * time.Second)
	for {
		select {
		case row := <-wait:
			if row[0] != "cookie" {
				return append(rows, row), nil
			}
			rows = append(rows, row)
		case <-timeout:
			return nil, errors.New("Cookie 操作超时")
		case <-u.quit:
			return nil, errors.New("应用已退出")
		}
	}
}

func (u *UI) WebviewCookies(p *plugin.Plugin, address string) ([]plugin.WebviewCookie, error) {
	rows, err := u.cookieRequest(p, "cookieGet", address)
	if err != nil {
		return nil, err
	}
	var out []plugin.WebviewCookie
	for _, row := range rows {
		if row[0] != "cookie" || len(row) < 8 {
			continue
		}
		expires, _ := strconv.ParseFloat(row[7], 64)
		out = append(out, plugin.WebviewCookie{Name: row[1], Value: row[2], Domain: row[3], Path: row[4], Secure: row[5] == "1", HTTPOnly: row[6] == "1", Expires: expires})
	}
	return out, nil
}

func (u *UI) SetWebviewCookie(p *plugin.Plugin, c plugin.WebviewCookie) error {
	rows, err := u.cookieRequest(p, "cookieSet", c.URL, c.Name, c.Value, c.Domain, c.Path, flag(c.Secure), flag(c.HTTPOnly), fmt.Sprintf("%.0f", c.Expires))
	if err != nil {
		return err
	}
	if last := rows[len(rows)-1]; len(last) < 2 || last[1] != "1" {
		return errors.New("Cookie 设置失败，请检查 url、domain 与取值")
	}
	return nil
}

func (u *UI) RemoveWebviewCookies(p *plugin.Plugin, address, name string) (int, error) {
	rows, err := u.cookieRequest(p, "cookieDel", address, name)
	if err != nil {
		return 0, err
	}
	count := 0
	if last := rows[len(rows)-1]; len(last) > 1 {
		count, _ = strconv.Atoi(last[1])
	}
	return count, nil
}
