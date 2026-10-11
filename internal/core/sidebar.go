package core

import (
	"errors"
	"log"
	"time"

	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

// 侧栏分区完全由扩展维护：标题、条目、点击与右键菜单都来自扩展导出的函数，
// 主程序只负责显示 getSidebar 的结果并把操作转交给扩展。

func (a *App) RefreshMenus() {
	go a.ReloadMenus()
}

// ReloadMenus 重新读取各扩展的侧栏分区。
func (a *App) ReloadMenus() {
	sidebars := map[string]*model.Sidebar{}
	for _, p := range a.Plugins.Running() {
		if !p.HasPermission(plugin.PermSidebar) || !p.Has("getSidebar") {
			continue
		}
		var s *model.Sidebar
		if err := p.CallInto(20*time.Second, &s, "getSidebar"); err != nil {
			log.Printf("getSidebar %s: %v", p.ID(), err)
			continue
		}
		if s != nil && s.Title != "" {
			sidebars[p.ID()] = s
		}
	}
	a.menuMu.Lock()
	a.sidebars = sidebars
	a.menuMu.Unlock()
	a.redraw()
}

func (a *App) Sidebar(pluginID string) *model.Sidebar {
	if p := a.Plugins.Get(pluginID); p == nil || !p.Running() || !p.HasPermission(plugin.PermSidebar) {
		return nil
	}
	a.menuMu.Lock()
	defer a.menuMu.Unlock()
	return a.sidebars[pluginID]
}

// sidebarCall runs an extension handler that may wait on the user (pickers, dialogs).
func (a *App) sidebarCall(pluginID, name string, args ...any) {
	p := a.Plugins.Get(pluginID)
	if p == nil {
		return
	}
	go func() {
		if _, err := p.Call(10*time.Minute, name, args...); err != nil && !errors.Is(err, plugin.ErrNotImplemented) {
			a.fail("操作失败", err)
		}
		a.ReloadMenus()
	}()
}

// SidebarAdd 触发扩展的「+」操作。
func (a *App) SidebarAdd(pluginID string) { a.sidebarCall(pluginID, "onSidebarAdd") }

// SidebarOpen 把条目的点击交给扩展。扩展返回歌曲列表时由主界面显示，否则点击已由扩展自行处理。
func (a *App) SidebarOpen(pluginID, itemID string) (*model.TrackList, error) {
	p := a.Plugins.Get(pluginID)
	if p == nil || !p.Running() {
		return nil, errors.New("插件未启用")
	}
	var list *model.TrackList
	if err := p.CallInto(10*time.Minute, &list, "onSidebarOpen", itemID); err != nil {
		return nil, err
	}
	if list == nil {
		return nil, nil
	}
	tracks, err := a.pluginTracks(p, list.Tracks)
	if err != nil {
		return nil, err
	}
	list.Tracks = tracks
	return list, nil
}

// pluginTracks validates the references an extension returned and fills in what the index already knows.
func (a *App) pluginTracks(p *plugin.Plugin, tracks []model.Track) ([]model.Track, error) {
	if err := a.Plugins.ValidateTracks(p, tracks); err != nil {
		return nil, err
	}
	for i := range tracks {
		if !a.Plugins.Scheme(tracks[i].Source).Has("getMusicInfo") {
			tracks[i].MetadataReady = true
		}
	}
	return a.cachedTrackRefs(tracks), nil
}

// SidebarMenu 返回扩展为条目提供的右键菜单。
func (a *App) SidebarMenu(pluginID, itemID string) ([]model.Action, error) {
	p := a.Plugins.Get(pluginID)
	if p == nil || !p.Has("getSidebarMenu") {
		return nil, nil
	}
	var items []model.Action
	err := p.CallInto(10*time.Second, &items, "getSidebarMenu", itemID)
	return items, err
}

func (a *App) SidebarMenuAction(pluginID, itemID, key string) {
	a.sidebarCall(pluginID, "onSidebarMenu", itemID, key)
}

// OpenSidebar lets an extension show one of its own entries (sim.sidebar.open).
// The section is read again first, so an entry the extension has just added is already listed.
// This runs off the extension's own thread: getSidebar is served once its current handler yields.
func (a *App) OpenSidebar(pluginID, itemID string) {
	go func() {
		a.ReloadMenus()
		if a.OnSidebarOpen != nil {
			a.OnSidebarOpen(pluginID, itemID)
		}
	}()
}

func (a *App) Confirm(text string) (bool, error) {
	if a.OnConfirm == nil {
		return false, errors.New("当前界面不支持确认对话框")
	}
	return a.OnConfirm(text)
}

const infoFailed = "很抱歉，歌曲信息获取失败。"

func trackTitle(t model.Track) string {
	if t.Title == "" {
		return "未知歌曲"
	}
	return t.Title
}

// sourceError records the detail of a failure in the run log of the extension that owns the
// song's protocol. The user only sees a fixed sentence: ids, paths and raw errors stay in that log.
func (a *App) sourceError(t model.Track, what string, err error) {
	log.Printf("%s (%s %q): %v", what, t.Source, t.ID, err)
	if a.Plugins == nil {
		return
	}
	if s := a.Plugins.Scheme(t.Source); s != nil && len(s.Bindings) > 0 {
		s.Bindings[0].Plugin.Logf("[error] %s（%s）：%v", what, t.ID, err)
	}
}

// MusicInfoFailed reports a failed information request to the extensions concerned and
// returns the sentence shown to the user instead of the raw error.
func (a *App) MusicInfoFailed(ts []model.Track, err error) error {
	seen := map[string]bool{}
	for _, t := range ts {
		if !seen[t.Source] {
			seen[t.Source] = true
			a.sourceError(t, "歌曲信息获取失败", err)
		}
	}
	return errors.New(infoFailed)
}
