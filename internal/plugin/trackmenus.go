package plugin

import (
	"errors"
	"simmusic/internal/model"
	"strings"
)

func (p *Plugin) CanDownload() bool {
	if p.Manifest.Capabilities.Download != nil {
		return *p.Manifest.Capabilities.Download && (p.Has("getMusicInfo") || p.Has("getDownload"))
	}
	return p.Has("getDownload")
}

func (p *Plugin) DownloadAllowed() bool {
	if !p.CanDownload() {
		return false
	}
	// Preserve legacy extension behavior; new capability declarations use song-menu grants.
	if p.Manifest.Capabilities.Download == nil && !p.Manifest.declares(PermTrackMenu) {
		return true
	}
	return p.TrackMenuGranted("download")
}

func (p *Plugin) TrackMenu(key string) (model.Action, bool) {
	if strings.HasPrefix(key, "scheme:") {
		parts := strings.SplitN(strings.TrimPrefix(key, "scheme:"), "/", 2)
		if len(parts) != 2 {
			return model.Action{}, false
		}
		for _, d := range append(append([]SchemeDeclaration(nil), p.Manifest.Schemes...), p.Manifest.SchemeExtensions...) {
			if d.Scheme != parts[0] {
				continue
			}
			if parts[1] == "download" {
				on := d.Download
				if on == nil {
					on = p.Manifest.Capabilities.Download
				}
				if on != nil && *on || on == nil && (d.Handlers["getDownload"] != "" || p.Has("getDownload")) {
					return model.Action{Key: key, Label: "下载曲目", Icon: "EC5A"}, true
				}
				return model.Action{}, false
			}
			items := append(append([]model.Action(nil), p.Manifest.TrackMenus...), d.TrackMenus...)
			item, ok := findTrackMenu(items, parts[1])
			item.Key = key
			return item, ok
		}
		return model.Action{}, false
	}
	if key == "download" && (p.Manifest.Capabilities.Download != nil && *p.Manifest.Capabilities.Download || p.Manifest.Capabilities.Download == nil && p.CanDownload()) {
		return model.Action{Key: "download", Label: "下载曲目", Icon: "EC5A", Scenes: []string{"list", "play", "queue"}}, true
	}
	return findTrackMenu(p.Manifest.TrackMenus, key)
}

func findTrackMenu(items []model.Action, key string) (model.Action, bool) {
	var find func([]model.Action, string) (model.Action, bool)
	find = func(items []model.Action, prefix string) (model.Action, bool) {
		for _, item := range items {
			path := prefix + item.Key
			if path == key {
				item.Key = path
				return item, true
			}
			if found, ok := find(item.Children, path+"/"); ok {
				return found, true
			}
		}
		return model.Action{}, false
	}
	return find(items, "")
}

func (p *Plugin) TrackMenuGranted(key string) bool {
	if !p.HasPermission(PermTrackMenu) {
		return false
	}
	if strings.HasPrefix(key, "scheme:") && !p.HasPermission(PermSchemeExtend) {
		for _, d := range p.Manifest.SchemeExtensions {
			if strings.HasPrefix(key, "scheme:"+d.Scheme+"/") {
				return false
			}
		}
	}
	parts := strings.Split(key, "/")
	for i := range parts {
		if i == 0 && strings.HasPrefix(parts[0], "scheme:") {
			continue
		}
		path := strings.Join(parts[:i+1], "/")
		item, ok := p.TrackMenu(path)
		if !ok {
			return false
		}
		if !p.TrackMenuPreference(path) {
			return false
		}
		for _, perm := range item.Permissions {
			if !p.HasPermission(perm) {
				return false
			}
		}
	}
	return true
}

// MenuDeclarations returns scoped declarations for the permission editor.
func (p *Plugin) MenuDeclarations() []model.Action {
	if len(p.Manifest.Schemes) == 0 && len(p.Manifest.SchemeExtensions) == 0 {
		items := append([]model.Action(nil), p.Manifest.TrackMenus...)
		if d, ok := p.TrackMenu("download"); ok {
			items = append(items, d)
		}
		return items
	}
	var out []model.Action
	declarations := append(append([]SchemeDeclaration(nil), p.Manifest.Schemes...), p.Manifest.SchemeExtensions...)
	for _, d := range declarations {
		prefix := "scheme:" + d.Scheme + "/"
		// The protocol name only disambiguates entries of multi-protocol extensions.
		label := ""
		if len(declarations) > 1 {
			label = d.Scheme + " · "
		}
		var scope func([]model.Action) []model.Action
		scope = func(items []model.Action) []model.Action {
			result := append([]model.Action(nil), items...)
			for i := range result {
				result[i].Children = scope(result[i].Children)
			}
			return result
		}
		items := scope(append(append([]model.Action(nil), p.Manifest.TrackMenus...), d.TrackMenus...))
		for i := range items {
			items[i].Key = prefix + items[i].Key
			items[i].Label = label + items[i].Label
		}
		if item, ok := p.TrackMenu(prefix + "download"); ok {
			item.Label = label + item.Label
			items = append(items, item)
		}
		out = append(out, items...)
	}
	return out
}

func (p *Plugin) SetTrackMenuGranted(key string, on bool) error {
	if _, ok := p.TrackMenu(key); !ok {
		return errors.New("扩展未声明此歌曲菜单")
	}
	p.mgr.enabled.Set("plugin.trackMenu."+p.ID()+"."+key, on)
	return nil
}

func (p *Plugin) TrackMenuPreference(key string) bool {
	on, ok := p.mgr.enabled.Get("plugin.trackMenu." + p.ID() + "." + key).(bool)
	if !ok && strings.HasPrefix(key, "scheme:") {
		if at := strings.Index(key, "/"); at >= 0 {
			on, ok = p.mgr.enabled.Get("plugin.trackMenu." + p.ID() + "." + key[at+1:]).(bool)
		}
	}
	return !ok || on
}
