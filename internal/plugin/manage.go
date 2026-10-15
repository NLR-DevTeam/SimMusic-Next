package plugin

import (
	"errors"

	"github.com/dop251/goja"
)

// Managing extensions from an extension (the "plugin" permission): sim.plugins lists what is installed and
// can install, remove, enable, disable and reload extensions, itself included. It is what an extension
// store needs. The four operations that change what runs on this machine are never carried out on the
// extension's word alone: each time, the host asks the user first.

// PluginAdmin carries out what an extension asks for, after asking the user where that is required.
// The first result of the asking operations is false when the user declined.
type PluginAdmin interface {
	PluginSetEnabled(by *Plugin, id string, on bool) (bool, error)
	PluginUninstall(by *Plugin, id string) (bool, error)
	// PluginInstall installs the .simex package at path, through the same installer dialog a user-chosen
	// package gets: the permissions are shown and can be switched off there.
	PluginInstall(by *Plugin, path string) (bool, error)
	PluginReload(by *Plugin, id string) error
}

// SetPluginAdmin connects the part of the host that can ask the user.
func (m *Manager) SetPluginAdmin(admin PluginAdmin) {
	m.mu.Lock()
	m.admin = admin
	m.mu.Unlock()
}

// Describe is what an extension with the permission learns about an installed extension.
func (p *Plugin) Describe() map[string]any {
	failure := ""
	if err := p.LoadErr(); err != nil {
		failure = err.Error()
	}
	return map[string]any{"id": p.ID(), "name": p.Manifest.Name, "version": p.Manifest.Version, "author": p.Manifest.Author,
		"description": p.Manifest.Description, "permissions": p.Manifest.Declared(), "builtin": p.Builtin, "development": p.Development,
		"enabled": p.mgr.IsEnabled(p.ID()), "running": p.Running(), "error": failure}
}

// Keeping runs a change to the set of extensions that p asked for. Such a change restarts the extensions
// in their load order; p itself is left running through it when it takes no part in that order (it
// provides no protocol), so that a store is not torn down by its own install button.
func (m *Manager) Keeping(p *Plugin, change func() error) error {
	m.keepMu.Lock()
	defer m.keepMu.Unlock()
	if len(p.Manifest.Schemes)+len(p.Manifest.SchemeExtensions) == 0 {
		m.mu.Lock()
		m.kept = p
		m.mu.Unlock()
		defer func() { m.mu.Lock(); m.kept = nil; m.mu.Unlock() }()
	}
	return change()
}

func (p *Plugin) installManage(sim *goja.Object) {
	rt := p.rt
	plugins := rt.NewObject()
	sim.Set("plugins", plugins)
	admin := func() PluginAdmin {
		p.need(PermPlugins)
		p.mgr.mu.RLock()
		admin := p.mgr.admin
		p.mgr.mu.RUnlock()
		if admin == nil {
			p.throw(errors.New("界面尚未就绪，暂时不能管理扩展"))
		}
		return admin
	}
	// list() and get(id): { id, name, version, author, description, permissions, builtin, development,
	// enabled, running, error }; get returns null for an extension that is not installed.
	plugins.Set("list", func() goja.Value {
		p.need(PermPlugins)
		list := []any{}
		for _, other := range p.mgr.All() {
			list = append(list, other.Describe())
		}
		return p.toJS(list)
	})
	plugins.Set("get", func(id string) goja.Value {
		p.need(PermPlugins)
		if other := p.mgr.Get(id); other != nil {
			return p.toJS(other.Describe())
		}
		return goja.Null()
	})
	// The asking operations resolve to whether the user allowed it; a refusal is not an error.
	plugins.Set("setEnabled", func(id string, on bool) goja.Value {
		a := admin()
		return p.async(func() (any, error) { return a.PluginSetEnabled(p, id, on) })
	})
	plugins.Set("uninstall", func(id string) goja.Value {
		a := admin()
		return p.async(func() (any, error) { return a.PluginUninstall(p, id) })
	})
	plugins.Set("install", func(path string) goja.Value {
		a := admin()
		path = p.checkPath(path)
		return p.async(func() (any, error) { return a.PluginInstall(p, path) })
	})
	plugins.Set("reload", func(id string) goja.Value {
		a := admin()
		return p.async(func() (any, error) { return true, a.PluginReload(p, id) })
	})
}
