package plugin

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"simmusic/internal/audio"
	"simmusic/internal/model"
	"simmusic/internal/winx"
)

// jsBuilder 允许异步任务在事件循环中构造 JS 值。
type jsBuilder func(rt *goja.Runtime) goja.Value

var httpClient = &http.Client{Timeout: 60 * time.Second}

// async 在后台 goroutine 执行 work，并返回一个 Promise。
func (p *Plugin) async(work func() (any, error)) goja.Value {
	rs := p.state()
	prom, resolve, reject := p.rt.NewPromise()
	go func() {
		var (
			v   any
			err error
		)
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%v", r)
				}
			}()
			v, err = work()
		}()
		p.postState(rs, func() {
			if err != nil {
				reject(p.rt.NewGoError(err))
			} else if b, ok := v.(jsBuilder); ok {
				resolve(b(p.rt))
			} else {
				resolve(p.toJS(v))
			}
			// 进入并离开一次 VM，确保 Promise 回调队列被执行
			_, _ = p.rt.RunString("void 0")
		})
	}()
	return p.rt.ToValue(prom)
}

func (p *Plugin) throw(err error) {
	panic(p.rt.NewGoError(err))
}

// checkPath 校验读取权限：插件自身数据目录始终允许，其他路径需要 fs.read。
func (p *Plugin) checkPath(path string) string { return p.checkAccess(path, PermFSRead) }

// checkWrite 校验写入权限：插件自身数据目录始终允许，其他路径需要 fs.write。
func (p *Plugin) checkWrite(path string) string { return p.checkAccess(path, PermFSWrite) }

func (p *Plugin) checkAccess(path, perm string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		p.throw(err)
	}
	if rel, err := filepath.Rel(p.DataDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return abs
	}
	p.need(perm)
	return abs
}

func (p *Plugin) need(perm string) {
	if !p.HasPermission(perm) {
		p.throw(p.permError(perm))
	}
}

func (p *Plugin) installAPI(rs *runState) {
	p.installFile()
	rt := p.rt
	sim := rt.NewObject()
	rt.Set("sim", sim)
	sim.Set("permissions", map[string]any{"has": func(name string) bool { return p.HasPermission(name) }, "menuAllowed": func(key string) bool { return p.TrackMenuGranted(key) }})

	// ----- 基本信息与日志 -----
	sim.Set("plugin", map[string]any{
		"id": p.Manifest.ID, "name": p.Manifest.Name, "version": p.Manifest.Version, "dataDir": p.DataDir, "dir": p.Dir,
	})
	sim.Set("platform", runtime.GOOS)
	logFn := func(level string) func(goja.FunctionCall) goja.Value {
		return func(c goja.FunctionCall) goja.Value {
			parts := make([]string, len(c.Arguments))
			for i, a := range c.Arguments {
				if o, ok := a.(*goja.Object); ok && o.ClassName() != "Function" {
					if b, err := o.MarshalJSON(); err == nil {
						parts[i] = string(b)
						continue
					}
				}
				parts[i] = a.String()
			}
			p.logf("%s%s", level, strings.Join(parts, " "))
			return goja.Undefined()
		}
	}
	sim.Set("log", logFn(""))
	console := rt.NewObject()
	console.Set("log", logFn(""))
	console.Set("info", logFn(""))
	console.Set("warn", logFn("[warn] "))
	console.Set("error", logFn("[error] "))
	rt.Set("console", console)

	// ----- 定时器 -----
	var (
		timerMu sync.Mutex
		timers  = map[int64]*time.Timer{}
		nextID  int64
	)
	go func() {
		<-rs.quit
		timerMu.Lock()
		defer timerMu.Unlock()
		for id, timer := range timers {
			timer.Stop()
			delete(timers, id)
		}
	}()
	addTimer := func(c goja.FunctionCall, repeat bool) goja.Value {
		fn, ok := goja.AssertFunction(c.Argument(0))
		if !ok {
			return goja.Undefined()
		}
		ms := c.Argument(1).ToInteger()
		if ms < 0 {
			ms = 0
		}
		timerMu.Lock()
		nextID++
		id := nextID
		var fire func()
		fire = func() {
			p.postState(rs, func() {
				timerMu.Lock()
				_, alive := timers[id]
				timerMu.Unlock()
				if !alive {
					return
				}
				if _, err := fn(goja.Undefined()); err != nil {
					p.logf("timer error: %v", err)
				}
				timerMu.Lock()
				if repeat {
					if _, still := timers[id]; still {
						timers[id] = time.AfterFunc(time.Duration(ms)*time.Millisecond, fire)
					}
				} else {
					delete(timers, id)
				}
				timerMu.Unlock()
			})
		}
		timers[id] = time.AfterFunc(time.Duration(ms)*time.Millisecond, fire)
		timerMu.Unlock()
		return rt.ToValue(id)
	}
	clearTimer := func(c goja.FunctionCall) goja.Value {
		id := c.Argument(0).ToInteger()
		timerMu.Lock()
		if t, ok := timers[id]; ok {
			t.Stop()
			delete(timers, id)
		}
		timerMu.Unlock()
		return goja.Undefined()
	}
	rt.Set("setTimeout", func(c goja.FunctionCall) goja.Value { return addTimer(c, false) })
	rt.Set("setInterval", func(c goja.FunctionCall) goja.Value { return addTimer(c, true) })
	rt.Set("clearTimeout", clearTimer)
	rt.Set("clearInterval", clearTimer)

	// ----- 网络 -----
	sim.Set("fetch", func(url string, opts map[string]any) goja.Value {
		p.need(PermNetwork)
		p.checkHost(url)
		method, _ := opts["method"].(string)
		if method == "" {
			method = "GET"
		}
		var body io.Reader
		if b, ok := opts["body"].(string); ok {
			body = strings.NewReader(b)
		}
		headers := map[string]string{}
		if h, ok := opts["headers"].(map[string]any); ok {
			for k, v := range h {
				headers[k] = fmt.Sprint(v)
			}
		}
		return p.async(func() (any, error) {
			req, err := http.NewRequest(strings.ToUpper(method), url, body)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "SimMusicNext/0.1")
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			if err != nil {
				return nil, err
			}
			hdr := map[string]any{}
			for k := range resp.Header {
				hdr[strings.ToLower(k)] = resp.Header.Get(k)
			}
			status, statusText, finalURL := resp.StatusCode, resp.Status, resp.Request.URL.String()
			return jsBuilder(func(rt *goja.Runtime) goja.Value {
				o := rt.NewObject()
				o.Set("ok", status >= 200 && status < 300)
				o.Set("status", status)
				o.Set("statusText", statusText)
				o.Set("url", finalURL)
				o.Set("headers", hdr)
				o.Set("text", func() string { return string(data) })
				o.Set("json", func() goja.Value {
					parse, _ := goja.AssertFunction(rt.Get("JSON").ToObject(rt).Get("parse"))
					v, err := parse(goja.Undefined(), rt.ToValue(string(data)))
					if err != nil {
						panic(rt.NewGoError(fmt.Errorf("JSON 解析失败: %w", err)))
					}
					return v
				})
				o.Set("base64", func() string { return base64.StdEncoding.EncodeToString(data) })
				o.Set("arrayBuffer", func() goja.Value { return rt.ToValue(rt.NewArrayBuffer(data)) })
				return o
			}), nil
		})
	})

	// ----- 文件系统 -----
	fsObj := rt.NewObject()
	fsObj.Set("file", func(path string) *goja.Object { return p.pathFile(path, false) })
	sim.Set("fs", fsObj)
	fsObj.Set("readText", func(path string) string {
		b, err := os.ReadFile(p.checkPath(path))
		if err != nil {
			p.throw(err)
		}
		return string(b)
	})
	fsObj.Set("writeText", func(path, content string) {
		path = p.checkWrite(path)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			p.throw(err)
		}
	})
	// writeFile(path, data) writes binary data: a File (one the host handed over, or made with new File),
	// an ArrayBuffer or a typed array; text is written as UTF-8.
	fsObj.Set("writeFile", func(path string, data goja.Value) {
		path = p.checkWrite(path)
		var content []byte
		switch v := data.Export().(type) {
		case string:
			content = []byte(v)
		case goja.ArrayBuffer:
			content = v.Bytes()
		case []uint8:
			content = v
		default:
			var resource model.Resource
			if err := rt.ExportTo(data, &resource); err != nil || resource.Path == "" && resource.Data == "" {
				p.throw(errors.New("writeFile 的数据应为 File、ArrayBuffer、TypedArray 或文本"))
			}
			var err error
			if content, err = p.fileBytes(resource); err != nil {
				p.throw(err)
			}
		}
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			p.throw(err)
		}
	})
	fsObj.Set("readBase64", func(path string) string {
		b, err := os.ReadFile(p.checkPath(path))
		if err != nil {
			p.throw(err)
		}
		return base64.StdEncoding.EncodeToString(b)
	})
	fsObj.Set("exists", func(path string) bool {
		_, err := os.Stat(p.checkPath(path))
		return err == nil
	})
	fsObj.Set("stat", func(path string) any {
		st, err := os.Stat(p.checkPath(path))
		if err != nil {
			return nil
		}
		return map[string]any{"size": st.Size(), "isDir": st.IsDir(), "mtime": st.ModTime().Unix()}
	})
	fsObj.Set("readDir", func(path string) any {
		path = p.checkPath(path)
		ents, err := os.ReadDir(path)
		if err != nil {
			p.throw(err)
		}
		out := make([]any, 0, len(ents))
		for _, e := range ents {
			info, _ := e.Info()
			var size, mtime int64
			if info != nil {
				size, mtime = info.Size(), info.ModTime().Unix()
			}
			out = append(out, map[string]any{
				"name": e.Name(), "path": filepath.Join(path, e.Name()), "isDir": e.IsDir(), "size": size, "mtime": mtime,
			})
		}
		return out
	})
	fsObj.Set("mkdir", func(path string) {
		if err := os.MkdirAll(p.checkWrite(path), 0o755); err != nil {
			p.throw(err)
		}
	})
	fsObj.Set("remove", func(path string) {
		if err := os.RemoveAll(p.checkWrite(path)); err != nil {
			p.throw(err)
		}
	})
	// walk(dir, exts) 递归列出指定扩展名的文件（异步）
	fsObj.Set("walk", func(dir string, exts []string) goja.Value {
		dir = p.checkPath(dir)
		set := map[string]bool{}
		for _, e := range exts {
			e = strings.ToLower(strings.TrimSpace(e))
			if e != "" && !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			set[e] = true
		}
		return p.async(func() (any, error) {
			var out []string
			err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if !d.IsDir() && (len(set) == 0 || set[strings.ToLower(filepath.Ext(path))]) {
					out = append(out, path)
				}
				return nil
			})
			sort.Strings(out)
			return out, err
		})
	})
	fsObj.Set("dirSize", func(path string) goja.Value {
		path = p.checkPath(path)
		return p.async(func() (any, error) {
			var total int64
			filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if info, err := d.Info(); err == nil {
						total += info.Size()
					}
				}
				return nil
			})
			return total, nil
		})
	})

	// ----- 路径工具 -----
	pathObj := rt.NewObject()
	sim.Set("path", pathObj)
	pathObj.Set("join", func(c goja.FunctionCall) goja.Value {
		parts := make([]string, len(c.Arguments))
		for i, a := range c.Arguments {
			parts[i] = a.String()
		}
		return rt.ToValue(filepath.Join(parts...))
	})
	pathObj.Set("basename", func(s string) string { return filepath.Base(s) })
	pathObj.Set("dirname", func(s string) string { return filepath.Dir(s) })
	pathObj.Set("extname", func(s string) string { return filepath.Ext(s) })
	pathObj.Set("sep", string(filepath.Separator))

	// ----- 设置 -----
	st := rt.NewObject()
	sim.Set("settings", st)
	st.Set("get", func(key string, def goja.Value) goja.Value {
		v := p.Settings.Get(key)
		if v == nil {
			return def
		}
		return p.toJS(v)
	})
	st.Set("set", func(key string, v goja.Value) {
		p.Settings.Set(key, exportJSON(v))
	})
	st.Set("all", func() goja.Value { return p.toJS(p.Settings.All()) })

	// ----- 媒体 -----
	media := rt.NewObject()
	media.Set("coverFile", func(path string) *goja.Object { return p.pathFile(path, true) })
	sim.Set("media", media)
	// readMetadata(paths, {duration: false}) reads tags only, without decoding audio.
	media.Set("readMetadata", func(paths []string, options map[string]any) goja.Value {
		for i := range paths {
			paths[i] = p.checkPath(paths[i])
		}
		read := audio.ReadMeta
		if options["duration"] == false {
			read = audio.ReadTags
		}
		return p.async(func() (any, error) {
			out := make([]audio.Meta, len(paths))
			var wg sync.WaitGroup
			sem := make(chan struct{}, max(2, runtime.NumCPU()))
			for i, path := range paths {
				wg.Add(1)
				sem <- struct{}{}
				go func() {
					defer func() { <-sem; wg.Done() }()
					out[i] = read(path)
				}()
			}
			wg.Wait()
			return out, nil
		})
	})
	media.Set("readLyrics", func(path string) string {
		return audio.ReadEmbeddedLyrics(p.checkPath(path))
	})
	// readTags(path, {cover: true}) returns every editable field; cover adds the picture data.
	media.Set("readTags", func(path string, options map[string]any) goja.Value {
		path = p.checkPath(path)
		withCover := options["cover"] == true
		return p.async(func() (any, error) { return audio.ReadFullTags(path, withCover) })
	})
	// writeTags(path, tags) rewrites only the fields given: "" deletes one, cover: null removes the
	// picture, cover: { path } or { base64 } replaces it. Writing a song's file is part of fs.write.
	media.Set("writeTags", func(path string, tags map[string]any) goja.Value {
		p.need(PermFSWrite)
		path, err := filepath.Abs(path)
		if err != nil {
			p.throw(err)
		}
		changes := audio.TagChanges{Text: map[string]string{}}
		for _, field := range audio.TagFields {
			if value, ok := tags[field]; ok && value != nil {
				changes.Text[field] = strings.TrimSpace(fmt.Sprint(value))
			}
		}
		if cover, ok := tags["cover"]; ok {
			changes.SetCover = true
			if picture, _ := cover.(map[string]any); picture != nil {
				if file, _ := picture["path"].(string); file != "" {
					if changes.Cover, err = os.ReadFile(p.checkPath(file)); err != nil {
						p.throw(err)
					}
				} else if encoded, _ := picture["base64"].(string); encoded != "" {
					if at := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && at >= 0 {
						encoded = encoded[at+1:]
					}
					if changes.Cover, err = base64.StdEncoding.DecodeString(encoded); err != nil {
						p.throw(errors.New("封面数据不是有效的 Base64"))
					}
				} else {
					p.throw(errors.New("cover 需要 path 或 base64"))
				}
			} else if cover != nil {
				p.throw(errors.New("cover 应为 null 或 { path } / { base64 }"))
			}
		}
		return p.async(func() (any, error) {
			// The player keeps the song it is playing open; it lets go while the file is replaced.
			if host, ok := p.mgr.Host.(interface{ ReleaseFile(string) func() }); ok {
				defer host.ReleaseFile(path)()
			}
			return nil, audio.WriteTags(path, changes)
		})
	})

	// ----- 对话框 -----
	dlg := rt.NewObject()
	sim.Set("dialog", dlg)
	dlg.Set("prompt", func(title, placeholder, value string) goja.Value {
		return p.async(func() (any, error) {
			host, ok := p.mgr.Host.(interface {
				Prompt(string, string, string) (string, error)
			})
			if !ok {
				return nil, errors.New("当前宿主不支持输入对话框")
			}
			return host.Prompt(title, placeholder, value)
		})
	})
	dlg.Set("confirm", func(text string) goja.Value {
		return p.async(func() (any, error) {
			host, ok := p.mgr.Host.(interface{ Confirm(string) (bool, error) })
			if !ok {
				return nil, errors.New("当前宿主不支持确认对话框")
			}
			return host.Confirm(text)
		})
	})
	dlg.Set("pickFolder", func(title string) goja.Value {
		return p.async(func() (any, error) {
			dir, err := p.mgr.Host.PickFolder(title)
			if err != nil || dir == "" {
				return nil, nil
			}
			return dir, nil
		})
	})
	dlg.Set("pickFiles", func(title string, exts []string) goja.Value {
		return p.async(func() (any, error) {
			fs, err := p.mgr.Host.PickFiles(title, exts)
			if err != nil {
				return []string{}, nil
			}
			return fs, nil
		})
	})

	// ----- 系统集成 -----
	shell := rt.NewObject()
	sim.Set("shell", shell)
	shell.Set("registerFileMenu", func(exts []string, enable bool, label string) bool {
		p.need(PermShell)
		if label == "" {
			label = "使用 SimMusic 播放"
		}
		if err := winx.RegisterFileMenu(exts, enable, label); err != nil {
			p.logf("registerFileMenu: %v", err)
			return false
		}
		return true
	})
	shell.Set("openPath", func(path string) { p.need(PermShell); winx.OpenPath(path) })
	shell.Set("showItemInFolder", func(path string) { p.need(PermShell); winx.RevealFile(path) })
	p.installShell(rs, shell)
	clipboard := rt.NewObject()
	clipboard.Set("writeText", func(text string) {
		p.need(PermClipboardWrite)
		if err := winx.CopyText(text); err != nil {
			p.throw(err)
		}
	})
	sim.Set("clipboard", clipboard)

	// ----- UI -----
	ui := rt.NewObject()
	sim.Set("ui", ui)
	// A toast is a short fixed notice; text of unbounded length is shown in a dialog instead.
	ui.Set("toast", func(msg string) {
		if len([]rune(msg)) > 10 {
			p.mgr.Host.Alert(msg)
			return
		}
		p.mgr.Host.Toast(msg)
	})
	ui.Set("alert", func(msg string) { p.mgr.Host.Alert(msg) })
	ui.Set("invalidateMusic", func() {
		if host, ok := p.mgr.Host.(interface{ ClearPluginMusicInfo(string) }); ok {
			host.ClearPluginMusicInfo(p.ID())
		}
	})
	ui.Set("refresh", func() { p.need(PermSidebar); p.mgr.Host.RefreshMenus() })
	// sim.index.clear(fields, ids) drops fields of this extension's own songs from the host's index, of
	// all of them when ids is omitted: the data a changed setting made stale is then requested again.
	index := rt.NewObject()
	sim.Set("index", index)
	index.Set("clear", func(fields, ids []string) {
		if host, ok := p.mgr.Host.(interface {
			ClearPluginFields(string, []string, []string)
		}); ok {
			host.ClearPluginFields(p.ID(), fields, ids)
		}
	})
	// sim.index.remove(ids) drops the whole index record of the extension's own songs, as the user's
	// "clear index" does for a song: everything about them is requested again.
	index.Set("remove", func(ids []string) {
		if host, ok := p.mgr.Host.(interface{ RemovePluginIndex(string, []string) }); ok {
			host.RemovePluginIndex(p.ID(), ids)
		}
	})
	// The extension maintains its own sidebar section: refresh re-reads getSidebar,
	// open shows one of its entries as if the user had clicked it.
	sidebar := rt.NewObject()
	sim.Set("sidebar", sidebar)
	sidebar.Set("refresh", func() { p.need(PermSidebar); p.mgr.Host.RefreshMenus() })
	sidebar.Set("open", func(id string) {
		p.need(PermSidebar)
		if host, ok := p.mgr.Host.(interface{ OpenSidebar(string, string) }); ok {
			host.OpenSidebar(p.ID(), id)
		}
	})
	p.installWebview(rs, sim)
	p.installPlayer(rs, sim)
	p.installManage(sim)
}
