// Package plugin 实现基于 goja 的 JS 插件系统。
//
// 每个插件运行在独立的 goja.Runtime 与事件循环 goroutine 中，
// 通过全局对象 `sim` 访问宿主提供的网络、文件、设置等 API。
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"simmusic/internal/config"
	"simmusic/internal/model"
)

// Manifest 对应插件目录下的 plugin.json。
type Manifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Author       string   `json:"author"`
	Description  string   `json:"description"`
	Main         string   `json:"main"`
	Permissions  []string `json:"permissions"`
	Hosts        []string `json:"hosts"` // 网络权限的域名白名单（可选，支持 *.example.com）
	Capabilities struct {
		Download *bool `json:"download,omitempty"`
	} `json:"capabilities"`
	TrackMenus       []model.Action      `json:"trackMenus,omitempty"`
	Schemes          []SchemeDeclaration `json:"schemes,omitempty"`
	SchemeExtensions []SchemeDeclaration `json:"schemeExtensions,omitempty"`
}

var ErrNotImplemented = errors.New("plugin: function not implemented")

type Plugin struct {
	Manifest    Manifest
	Dir         string
	DataDir     string
	Builtin     bool
	Development bool
	Settings    *config.Store

	mgr     *Manager
	src     fs.FS // 插件代码所在文件系统（内置插件为 embed.FS）
	rt      *goja.Runtime
	exports *goja.Object
	logs    []string
	logMu   sync.Mutex

	mu      sync.Mutex
	rs      *runState
	loadErr error
}

// runState 是插件一次运行（start 到 stop）的状态。
type runState struct {
	jobs    chan func()
	quit    chan struct{}
	done    chan struct{}
	runtime *goja.Runtime
	names   map[string]bool // 导出的函数名
}

func (p *Plugin) state() *runState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rs
}

// LoadErr 返回最近一次加载错误。
func (p *Plugin) LoadErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loadErr
}

func (p *Plugin) setErr(err error) {
	p.mu.Lock()
	p.loadErr = err
	p.mu.Unlock()
}

func (p *Plugin) ID() string { return p.Manifest.ID }

func (p *Plugin) Logs() []string {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	return append([]string(nil), p.logs...)
}

func (p *Plugin) logf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	log.Printf("[plugin %s] %s", p.Manifest.ID, s)
	p.logMu.Lock()
	p.logs = append(p.logs, time.Now().Format("15:04:05 ")+s)
	if len(p.logs) > 200 {
		p.logs = p.logs[len(p.logs)-200:]
	}
	p.logMu.Unlock()
}

// post 将任务投递到插件事件循环。
func (p *Plugin) post(f func()) bool {
	return p.postState(p.state(), f)
}

func (p *Plugin) postState(rs *runState, f func()) bool {
	if rs == nil {
		return false
	}
	select {
	case <-rs.quit:
		return false
	default:
	}
	select {
	case rs.jobs <- f:
		return true
	case <-rs.quit:
		return false
	}
}

func (p *Plugin) loop(rs *runState) {
	defer close(rs.done)
	for {
		select {
		case f := <-rs.jobs:
			select {
			case <-rs.quit:
				return
			default:
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						p.logf("panic: %v", r)
					}
				}()
				f()
			}()
		case <-rs.quit:
			return
		}
	}
}

// start 创建运行时并执行入口脚本。
func (p *Plugin) start() error {
	p.stop()
	rs := &runState{jobs: make(chan func(), 256), quit: make(chan struct{}), done: make(chan struct{}), names: map[string]bool{}}
	p.mu.Lock()
	p.rs = rs
	p.mu.Unlock()
	go p.loop(rs)
	errCh := make(chan error, 1)
	p.postState(rs, func() {
		rt := goja.New()
		rt.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
		p.mu.Lock()
		rs.runtime = rt
		p.rt = rt
		p.mu.Unlock()
		select {
		case <-rs.quit:
			errCh <- errors.New("plugin stopped")
			return
		default:
		}
		p.installAPI(rs)
		main := p.Manifest.Main
		if main == "" {
			main = "main.js"
		}
		code, err := fs.ReadFile(p.src, main)
		if err != nil {
			errCh <- err
			return
		}
		module := rt.NewObject()
		exports := rt.NewObject()
		module.Set("exports", exports)
		wrapped := "(function(module, exports){" + string(code) + "\n})"
		fnv, err := rt.RunScript(p.Manifest.ID+"/"+main, wrapped)
		if err != nil {
			errCh <- err
			return
		}
		fn, _ := goja.AssertFunction(fnv)
		if _, err := fn(goja.Undefined(), module, exports); err != nil {
			errCh <- err
			return
		}
		p.exports = module.Get("exports").ToObject(rt)
		names := map[string]bool{}
		for _, k := range p.exports.Keys() {
			if _, ok := goja.AssertFunction(p.exports.Get(k)); ok {
				names[k] = true
			}
		}
		p.mu.Lock()
		rs.names = names
		p.mu.Unlock()
		errCh <- nil
	})
	select {
	case err := <-errCh:
		if err != nil {
			p.stop()
			return err
		}
	case <-time.After(10 * time.Second):
		p.stop()
		return errors.New("plugin load timeout")
	}
	// 调用可选的 init
	if _, err := p.Call(30*time.Second, "init"); err != nil && !errors.Is(err, ErrNotImplemented) {
		p.logf("init error: %v", err)
		p.stop()
		return err
	}
	if err := p.mgr.registerSchemes(p); err != nil {
		p.logf("协议注册失败：%v", err)
		p.stop()
		return err
	}
	return nil
}

func (p *Plugin) stop() {
	p.mgr.unregisterSchemes(p)
	p.mu.Lock()
	rs := p.rs
	var rt *goja.Runtime
	if rs != nil {
		rt = rs.runtime
		close(rs.quit)
		p.rs = nil
	}
	p.mu.Unlock()
	if rs != nil {
		if rt != nil {
			rt.Interrupt(errors.New("plugin stopped"))
		}
		<-rs.done
	}
}

func (p *Plugin) Running() bool { return p.state() != nil }

// Has 判断插件是否导出了指定函数。
func (p *Plugin) Has(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rs != nil && p.rs.names[name]
}

// Call 调用插件导出函数；返回值为 JSON 兼容的 Go 值。若函数返回 Promise 则等待其完成。
func (p *Plugin) Call(timeout time.Duration, name string, args ...any) (any, error) {
	if name == "getDownload" || name == "getMusicInfo" && len(args) > 1 && args[1] == "download" {
		if !p.DownloadAllowed() {
			return nil, errors.New("扩展不支持下载或下载菜单权限已被撤销")
		}
	}
	switch name {
	case "getTrackActions", "onTrackAction":
		if !p.HasPermission(PermTrackMenu) {
			return nil, p.permError(PermTrackMenu)
		}
	case "getSidebar", "onSidebarAdd", "onSidebarOpen", "getSidebarMenu", "onSidebarMenu":
		if !p.HasPermission(PermSidebar) {
			return nil, p.permError(PermSidebar)
		}
	}
	if !p.Running() {
		return nil, errors.New("plugin not running")
	}
	return p.call(timeout, name, args...)
}

func (p *Plugin) call(timeout time.Duration, name string, args ...any) (any, error) {
	rs := p.state()
	if rs == nil {
		return nil, errors.New("plugin stopped")
	}
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	done := func(v goja.Value, err error) {
		if err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{exportJSON(v), nil}
	}
	ok := p.postState(rs, func() {
		fn, ok := goja.AssertFunction(p.exports.Get(name))
		if !ok {
			ch <- result{nil, ErrNotImplemented}
			return
		}
		jsArgs := make([]goja.Value, len(args))
		for i, a := range args {
			jsArgs[i] = p.toJS(a)
		}
		v, err := fn(p.exports, jsArgs...)
		if err != nil {
			done(nil, err)
			return
		}
		prom, isProm := v.Export().(*goja.Promise)
		if !isProm {
			done(v, nil)
			return
		}
		switch prom.State() {
		case goja.PromiseStateFulfilled:
			done(prom.Result(), nil)
		case goja.PromiseStateRejected:
			done(nil, jsError(prom.Result()))
		default:
			then, _ := goja.AssertFunction(p.rt.ToValue(prom).ToObject(p.rt).Get("then"))
			then(p.rt.ToValue(prom),
				p.rt.ToValue(func(v goja.Value) { done(v, nil) }),
				p.rt.ToValue(func(v goja.Value) { done(nil, jsError(v)) }))
		}
	})
	if !ok {
		return nil, errors.New("plugin stopped")
	}
	select {
	case r := <-ch:
		if r.err != nil && !errors.Is(r.err, ErrNotImplemented) {
			p.logf("%s() 出错: %v", name, r.err)
		}
		return r.v, r.err
	case <-rs.quit:
		return nil, errors.New("plugin stopped")
	case <-time.After(timeout):
		p.logf("%s() 超时", name)
		err := fmt.Errorf("plugin %s: %s timeout", p.Manifest.ID, name)
		p.stop()
		p.setErr(err)
		return nil, err
	}
}

// CallInto 调用并将结果解码到 out。
func (p *Plugin) CallInto(timeout time.Duration, out any, name string, args ...any) error {
	v, err := p.Call(timeout, name, args...)
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func jsError(v goja.Value) error {
	if v == nil {
		return errors.New("rejected")
	}
	if o, ok := v.(*goja.Object); ok {
		if m := o.Get("message"); m != nil && !goja.IsUndefined(m) {
			return errors.New(m.String())
		}
	}
	return errors.New(v.String())
}

// toJS 通过 JSON 转换将 Go 值转为普通 JS 对象（避免 Go 结构体包装）。
func (p *Plugin) toJS(v any) goja.Value {
	switch v.(type) {
	case nil:
		return goja.Undefined()
	case string, bool, int, int64, float64:
		return p.rt.ToValue(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return goja.Undefined()
	}
	// 用 JSON.parse 生成原生 JS 对象 / 数组，保证 push 等操作行为与普通 JS 一致
	parse, _ := goja.AssertFunction(p.rt.Get("JSON").ToObject(p.rt).Get("parse"))
	out, err := parse(goja.Undefined(), p.rt.ToValue(string(b)))
	if err != nil {
		return goja.Undefined()
	}
	return out
}

func exportJSON(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return v.Export()
}

// ---------- Manager ----------

type Manager struct {
	mu          sync.RWMutex
	plugins     []*Plugin
	baseData    string
	userDir     string
	Host        Host
	enabled     *config.Store
	schemeMu    sync.RWMutex
	schemes     map[string]*Scheme
	lifecycleMu sync.Mutex
	loading     bool
	webviews    WebviewBackend
}

// Host 是插件可调用的宿主能力（由 core 实现）。
type Host interface {
	Toast(msg string)
	Alert(msg string)
	RefreshMenus()
	PickFolder(title string) (string, error)
	PickFiles(title string, exts []string) ([]string, error)
}

func NewManager(dataDir, userPluginDir string, enabled *config.Store, host Host) *Manager {
	return &Manager{baseData: dataDir, userDir: userPluginDir, enabled: enabled, Host: host, schemes: map[string]*Scheme{}}
}

func (m *Manager) UserDir() string { return m.userDir }

// AddBuiltin 注册内置插件（代码位于 fsys 根目录）。
func (m *Manager) AddBuiltin(fsys fs.FS) error {
	return m.add(fsys, "", true)
}

func (m *Manager) AddBuiltinDir(dir string) error { return m.add(os.DirFS(dir), dir, true) }

func (m *Manager) BeginLoading() { m.loading = true }

// LoadUserPlugins 扫描用户插件目录。
func (m *Manager) LoadUserPlugins() {
	m.loading = true
	var development map[string]string
	m.enabled.Unmarshal("plugin.development", &development)
	for id, dir := range development {
		man, err := ReadManifest(dir)
		if err != nil || man.ID != id {
			log.Printf("load development plugin %s: invalid or missing manifest", id)
			continue
		}
		if err := m.add(os.DirFS(dir), dir, false); err != nil {
			log.Printf("load development plugin %s: %v", id, err)
		} else {
			m.Get(id).Development = true
		}
	}
	_ = os.MkdirAll(m.userDir, 0o755)
	ents, _ := os.ReadDir(m.userDir)
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(m.userDir, e.Name())
		registered := false
		for _, p := range m.All() {
			if p.Dir == dir {
				registered = true
				break
			}
		}
		if registered {
			continue
		}
		if err := m.add(os.DirFS(dir), dir, false); err != nil {
			log.Printf("load plugin %s: %v", dir, err)
		}
	}
	m.loading = false
	m.RestartOrdered()
}

func (m *Manager) add(fsys fs.FS, dir string, builtin bool) error {
	b, err := fs.ReadFile(fsys, "plugin.json")
	if err != nil {
		return err
	}
	var man Manifest
	if err := json.Unmarshal(b, &man); err != nil {
		return err
	}
	if err := validateManifest(man); err != nil {
		return err
	}
	if m.Get(man.ID) != nil {
		return fmt.Errorf("插件 %s 已存在", man.ID)
	}
	p := &Plugin{Manifest: man, Dir: dir, Builtin: builtin, mgr: m, src: fsys}
	m.migrateSidebarGrant(man.ID)
	p.DataDir = filepath.Join(m.baseData, man.ID)
	_ = os.MkdirAll(p.DataDir, 0o755)
	p.Settings = config.Open(filepath.Join(p.DataDir, "settings.json"), nil)
	m.mu.Lock()
	m.plugins = append(m.plugins, p)
	m.mu.Unlock()
	if !m.loading && m.IsEnabled(man.ID) {
		if err := p.start(); err != nil {
			p.setErr(err)
			log.Printf("start plugin %s: %v", man.ID, err)
		}
	}
	return nil
}

func (m *Manager) IsEnabled(id string) bool {
	v := m.enabled.Get("plugin.enabled." + id)
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func (m *Manager) SetEnabled(id string, on bool) error {
	m.enabled.Set("plugin.enabled."+id, on)
	m.RestartOrdered()
	return nil
}

// Reload 重启插件（用于开发调试）。
func (m *Manager) Reload(id string) error {
	p := m.Get(id)
	if p == nil {
		return errors.New("not found")
	}
	if p.Dir != "" {
		manifest, err := ReadManifest(p.Dir)
		if err != nil {
			return err
		}
		if manifest.ID != id {
			return errors.New("重新加载时不能更改扩展 ID")
		}
		fresh := &Plugin{Manifest: manifest, Dir: p.Dir, DataDir: p.DataDir, Builtin: p.Builtin, Development: p.Development, Settings: p.Settings, mgr: m, src: os.DirFS(p.Dir)}
		p.stop()
		m.mu.Lock()
		for i, item := range m.plugins {
			if item == p {
				m.plugins[i] = fresh
				break
			}
		}
		m.mu.Unlock()
		p = fresh
	}
	m.RestartOrdered()
	return p.LoadErr()
}

// Install 从目录安装插件（复制到用户插件目录）。
// 调用前应先通过 ReadManifest 向用户展示并确认权限；安装时授予其声明的全部权限。
func (m *Manager) Install(srcDir string, denied ...string) (*Plugin, error) {
	man, err := ReadManifest(srcDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(srcDir, man.Main)); err != nil {
		return nil, fmt.Errorf("扩展入口不存在：%w", err)
	}
	if err := os.MkdirAll(m.userDir, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(m.userDir, ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := copyDir(srcDir, stage); err != nil {
		return nil, err
	}
	if old := m.Get(man.ID); old != nil {
		if old.Builtin {
			return nil, errors.New("不能覆盖内置插件")
		}
		if err := m.Remove(man.ID); err != nil {
			return nil, err
		}
	}
	dst := filepath.Join(m.userDir, man.ID)
	if err := os.Rename(stage, dst); err != nil {
		return nil, err
	}
	m.GrantAll(man, denied...)
	if err := m.add(os.DirFS(dst), dst, false); err != nil {
		return nil, err
	}
	m.RestartOrdered()
	return m.Get(man.ID), nil
}

func (m *Manager) Remove(id string) error {
	p := m.Get(id)
	if p == nil || p.Builtin {
		return errors.New("无法删除")
	}
	p.stop()
	m.mu.Lock()
	for i, x := range m.plugins {
		if x == p {
			m.plugins = append(m.plugins[:i], m.plugins[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	if p.Development {
		m.setDevelopment(id, "")
		m.RestartOrdered()
		return nil
	}
	m.RestartOrdered()
	return os.RemoveAll(p.Dir)
}

func (m *Manager) Get(id string) *Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.plugins {
		if p.Manifest.ID == id {
			return p
		}
	}
	return nil
}

func (m *Manager) All() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]*Plugin(nil), m.plugins...)
}

// Running 返回所有运行中的插件。
func (m *Manager) Running() []*Plugin {
	var out []*Plugin
	for _, p := range m.All() {
		if p.Running() {
			out = append(out, p)
		}
	}
	return out
}

func (m *Manager) StopAll() {
	for _, p := range m.All() {
		p.stop()
	}
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("扩展包不支持符号链接")
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// Logf 向扩展的运行日志写入一条宿主记录。
func (p *Plugin) Logf(format string, args ...any) { p.logf(format, args...) }
