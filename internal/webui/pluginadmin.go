package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"simmusic/internal/plugin"
)

// What an extension with the "plugin" permission asks for (sim.plugins). Installing, removing, enabling and
// disabling are the user's decisions: each request is shown and carried out only on a yes, every time.

func (u *UI) pluginsChanged() {
	u.app.InvalidateMusicResources(nil)
	go u.app.ReloadMenus()
}

func (u *UI) askPluginChange(by, target *plugin.Plugin, verb string) (bool, error) {
	if by == target {
		return u.Confirm(fmt.Sprintf("扩展「%s」请求%s它自己，是否允许？", by.Manifest.Name, verb))
	}
	return u.Confirm(fmt.Sprintf("扩展「%s」请求%s扩展「%s」，是否允许？", by.Manifest.Name, verb, target.Manifest.Name))
}

func (u *UI) PluginSetEnabled(by *plugin.Plugin, id string, on bool) (bool, error) {
	plugins := u.app.Plugins
	target := plugins.Get(id)
	if target == nil {
		return false, errors.New("扩展不存在")
	}
	if plugins.IsEnabled(id) == on {
		return true, nil
	}
	verb := "停用"
	if on {
		verb = "启用"
	}
	if allowed, err := u.askPluginChange(by, target, verb); err != nil || !allowed {
		return false, err
	}
	if err := plugins.Keeping(by, func() error { return plugins.SetEnabled(id, on) }); err != nil {
		return false, err
	}
	u.pluginsChanged()
	return true, nil
}

func (u *UI) PluginUninstall(by *plugin.Plugin, id string) (bool, error) {
	plugins := u.app.Plugins
	target := plugins.Get(id)
	if target == nil {
		return false, errors.New("扩展不存在")
	}
	if target.Builtin {
		return false, errors.New("不能卸载内置扩展")
	}
	if allowed, err := u.askPluginChange(by, target, "卸载"); err != nil || !allowed {
		return false, err
	}
	if err := plugins.Keeping(by, func() error { return plugins.Remove(id) }); err != nil {
		return false, err
	}
	u.pluginsChanged()
	return true, nil
}

// PluginInstall shows the package in the installer dialog, as one the user picked is shown, with the name
// of the extension that asks. The answer is the permissions the user switched off, or nothing for a refusal.
func (u *UI) PluginInstall(by *plugin.Plugin, path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, errors.New("无法读取扩展包")
	}
	dir, manifest, err := plugin.PreparePackage(file)
	file.Close()
	if err != nil {
		return false, err
	}
	review, err := u.setInstall(dir, manifest, false)
	if err != nil {
		return false, err
	}
	token, _ := review["installToken"].(string)
	review["requestedBy"] = by.Manifest.Name
	shown, _ := json.Marshal(review)
	answer, err := u.ask(&promptRequest{Kind: "install", Value: string(shown)})
	var denied []string
	if err != nil || answer == "" || json.Unmarshal([]byte(answer), &denied) != nil {
		u.mu.Lock()
		current := u.pendingInstallToken == token
		u.mu.Unlock()
		if current {
			u.clearInstall()
		}
		return false, err
	}
	if err := u.app.Plugins.Keeping(by, func() error { return u.finishInstall(token, denied) }); err != nil {
		return false, err
	}
	return true, nil
}

func (u *UI) PluginReload(by *plugin.Plugin, id string) error {
	plugins := u.app.Plugins
	if plugins.Get(id) == nil {
		return errors.New("扩展不存在")
	}
	if err := plugins.Keeping(by, func() error { return plugins.Reload(id) }); err != nil {
		return err
	}
	u.pluginsChanged()
	return nil
}

// finishInstall installs the package that was reviewed under this token, with the permissions left on.
func (u *UI) finishInstall(token string, denied []string) error {
	u.mu.Lock()
	if token == "" || token != u.pendingInstallToken {
		u.mu.Unlock()
		return errors.New("请重新选择扩展并确认权限")
	}
	dir := u.pendingInstall
	development, reviewed := u.pendingDevelopment, u.pendingManifest
	u.pendingInstall = ""
	u.pendingInstallToken = ""
	u.mu.Unlock()
	if dir == "" {
		return errors.New("请先选择扩展并确认权限")
	}
	if !development {
		defer os.RemoveAll(dir)
	}
	current, err := plugin.ReadManifest(dir)
	if err != nil {
		return err
	}
	if !plugin.SameManifest(current, reviewed) {
		return errors.New("扩展声明已改变，请重新选择并确认权限")
	}
	if development {
		_, err = u.app.Plugins.LoadDevelopment(dir, denied...)
	} else {
		_, err = u.app.Plugins.Install(dir, denied...)
	}
	if err != nil {
		return err
	}
	u.pluginsChanged()
	return nil
}
