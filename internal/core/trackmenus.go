package core

import (
	"errors"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
	"strings"
	"time"
)

func menuMatches(item model.Action, scene string, count int) bool {
	if item.Selection == "single" && count != 1 || item.Selection == "multiple" && count < 2 {
		return false
	}
	if len(item.Scenes) == 0 {
		return true
	}
	for _, value := range item.Scenes {
		if scene == value {
			return true
		}
	}
	return false
}

func (a *App) trackScheme(ts []model.Track) *plugin.Scheme {
	if len(ts) == 0 {
		return nil
	}
	for _, t := range ts {
		if t.Source != ts[0].Source {
			return nil
		}
	}
	return a.Plugins.Scheme(ts[0].Source)
}

func (a *App) SongActions(ts []model.Track, scene, parent string) ([]model.Action, error) {
	s := a.trackScheme(ts)
	if s == nil {
		return nil, nil
	}
	if scene != "list" && scene != "play" && scene != "queue" {
		return nil, errors.New("未知歌曲菜单场景")
	}
	var out []model.Action
	for _, b := range s.Bindings {
		p := b.Plugin
		if !p.HasPermission(plugin.PermTrackMenu) || b.Extension && !p.HasPermission(plugin.PermSchemeExtend) {
			continue
		}
		namespace := ""
		if b.Extension {
			namespace = p.ID() + "::"
		}
		if parent != "" && (namespace != "" && !strings.HasPrefix(parent, namespace) || namespace == "" && strings.Contains(parent, "::")) {
			continue
		}
		local := strings.TrimPrefix(parent, namespace)
		items, err := a.songActionsBinding(b, ts, scene, local)
		if err != nil {
			return nil, err
		}
		var qualify func([]model.Action)
		qualify = func(items []model.Action) {
			for i := range items {
				items[i].Key = namespace + items[i].Key
				qualify(items[i].Children)
			}
		}
		qualify(items)
		out = append(out, items...)
	}
	return out, nil
}

func (a *App) songActionsBinding(b plugin.SchemeBinding, ts []model.Track, scene, parent string) ([]model.Action, error) {
	p := b.Plugin
	items := b.Menus()
	if parent != "" {
		parts := strings.Split(parent, "/")
		for i := range parts {
			if i == 0 && strings.HasPrefix(parts[0], "scheme:") {
				continue
			}
			ancestor, ok := p.TrackMenu(strings.Join(parts[:i+1], "/"))
			if !ok || !menuMatches(ancestor, scene, len(ts)) {
				return nil, errors.New("歌曲菜单场景不匹配")
			}
		}
		item, ok := p.TrackMenu(parent)
		if !ok || !p.TrackMenuGranted(parent) || !menuMatches(item, scene, len(ts)) {
			return nil, errors.New("歌曲菜单未获授权")
		}
		items = item.Children
		if item.Dynamic {
			fn := b.Declaration.Handlers["getTrackActions"]
			if fn == "" {
				fn = "getTrackActions"
			}
			if err := p.CallInto(10*time.Second, &items, fn, ts, scene, strings.TrimPrefix(parent, b.MenuPrefix()), b.Declaration.Scheme); err != nil {
				return nil, err
			}
		}
	}
	var filter func([]model.Action, string, bool) []model.Action
	filter = func(items []model.Action, prefix string, dynamic bool) []model.Action {
		var out []model.Action
		for _, item := range items {
			if item.Key == "" || strings.Contains(item.Key, "/") {
				continue
			}
			item.Key = prefix + item.Key
			if !menuMatches(item, scene, len(ts)) {
				continue
			}
			if !dynamic && !p.TrackMenuGranted(item.Key) {
				continue
			}
			allowed := true
			for _, perm := range item.Permissions {
				if !p.HasPermission(perm) {
					allowed = false
				}
			}
			if !allowed {
				continue
			}
			item.Children = filter(item.Children, item.Key+"/", dynamic)
			out = append(out, item)
		}
		return out
	}
	prefix := b.MenuPrefix()
	if parent != "" {
		prefix = parent + "/"
	}
	dynamic := false
	if item, ok := p.TrackMenu(parent); ok {
		dynamic = item.Dynamic
	}
	return filter(items, prefix, dynamic), nil
}

// Execution repeats declaration, scene and grant checks; a fabricated API key cannot bypass the UI.
func (a *App) ExecuteSongAction(ts []model.Track, key, scene string) error {
	s := a.trackScheme(ts)
	if s == nil {
		return errors.New("曲目必须来自同一个已启用扩展")
	}
	parent := ""
	if at := strings.LastIndex(key, "/"); at >= 0 {
		parent = key[:at]
		// "scheme:<name>/" is the protocol prefix of a top-level menu, not a parent menu.
		local := parent
		if cut := strings.Index(local, "::"); cut >= 0 {
			local = local[cut+2:]
		}
		if strings.HasPrefix(local, "scheme:") && !strings.Contains(local, "/") {
			parent = ""
		}
	}
	items, err := a.SongActions(ts, scene, parent)
	if err != nil {
		return err
	}
	var find func([]model.Action) bool
	find = func(items []model.Action) bool {
		for _, item := range items {
			if item.Key == key && len(item.Children) == 0 && !item.Dynamic {
				return true
			}
			if find(item.Children) {
				return true
			}
		}
		return false
	}
	if !find(items) {
		return errors.New("歌曲菜单未声明、场景不匹配或权限已被撤销")
	}
	for _, b := range s.Bindings {
		namespace := ""
		if b.Extension {
			namespace = b.Plugin.ID() + "::"
		}
		if namespace != "" && !strings.HasPrefix(key, namespace) || namespace == "" && strings.Contains(key, "::") {
			continue
		}
		local := strings.TrimPrefix(key, namespace)
		if !strings.HasPrefix(local, b.MenuPrefix()) {
			continue
		}
		fn := b.Declaration.Handlers["onTrackAction"]
		if fn == "" {
			fn = "onTrackAction"
		}
		_, err = b.Plugin.Call(time.Minute, fn, strings.TrimPrefix(local, b.MenuPrefix()), ts, scene, s.Name)
		return err
	}
	return errors.New("歌曲菜单的协议处理者已卸载")
}
