package core

import (
	"slices"
	"strings"

	"simmusic/internal/model"
)

// ClearPluginMusicInfo is the extension's sim.ui.invalidateMusic: the resources of the protocols it
// handles are requested again. Plain data stays in the index until a request returns it anew.
func (a *App) ClearPluginMusicInfo(id string) {
	a.InvalidateMusicResources(func(key string) bool {
		source := strings.SplitN(key, "\x00", 2)[0]
		scheme := a.Plugins.Scheme(source)
		if scheme == nil {
			return source == id
		}
		for _, binding := range scheme.Bindings {
			if binding.Plugin.ID() == id {
				return true
			}
		}
		return false
	})
	if a.OnMusicInvalidated != nil {
		a.OnMusicInvalidated(id)
	}
}

// pluginOwns reports whether an extension registered or extends the protocol of a song.
func (a *App) pluginOwns(id, source string) bool {
	scheme := a.Plugins.Scheme(source)
	if scheme == nil {
		return source == id
	}
	for _, binding := range scheme.Bindings {
		if binding.Plugin.ID() == id {
			return true
		}
	}
	return false
}

// RemovePluginIndex is the extension's sim.index.remove: the index records of the given songs of
// its own are deleted and read again, the way the user clears the index of a song. An extension
// uses it when the song itself changed, say after its file's metadata was rewritten.
func (a *App) RemovePluginIndex(id string, ids []string) {
	if len(ids) == 0 {
		return
	}
	var tracks []model.Track
	x := a.musicIndex()
	x.mu.Lock()
	for key := range x.entries {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) == 2 && a.pluginOwns(id, parts[0]) && slices.Contains(ids, parts[1]) {
			tracks = append(tracks, model.Track{Source: parts[0], ID: parts[1]})
		}
	}
	x.mu.Unlock()
	go func() {
		if len(tracks) > 0 {
			if _, err := a.ClearMusicInfo(tracks, "list"); err != nil {
				a.sourceError(tracks[0], "歌曲信息获取失败", err)
			}
		}
		if a.OnMusicInvalidated != nil {
			a.OnMusicInvalidated(id)
		}
	}()
}

// playFields and downloadFields are supplied by the play and download scenes; clearing one of them
// only makes that scene be requested again. Any other field belongs to the list scene.
var playFields = []string{"playback", "lyrics", "format", "largeCover"}
var downloadFields = []string{"download", "ext"}

// ClearPluginFields is the extension's sim.index.clear: it drops the named fields from the index
// records of its own songs, of the given ids or of all of them, so they are requested again.
// An extension uses it when a setting makes specific data stale, say playback addresses after a quality change.
func (a *App) ClearPluginFields(id string, fields, ids []string) {
	if len(fields) == 0 {
		return
	}
	owned := func(source string) bool {
		scheme := a.Plugins.Scheme(source)
		if scheme == nil {
			return source == id
		}
		for _, binding := range scheme.Bindings {
			if binding.Plugin.ID() == id {
				return true
			}
		}
		return false
	}
	list := false
	for _, field := range fields {
		if !slices.Contains(playFields, field) && !slices.Contains(downloadFields, field) {
			list = true
		}
	}
	x := a.musicIndex()
	x.mu.Lock()
	for key, e := range x.entries {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || !owned(parts[0]) || ids != nil && !slices.Contains(ids, parts[1]) {
			continue
		}
		for _, field := range fields {
			delete(e.Fields, field)
			switch {
			case field == "lyrics":
				// Lyrics are shared by playback and download; neither scene may keep using the old text.
				delete(e.Scenes, "play")
				delete(e.Scenes, "download")
			case slices.Contains(playFields, field):
				delete(e.Scenes, "play")
			case slices.Contains(downloadFields, field):
				delete(e.Scenes, "download")
			default:
				e.Scenes = map[string]bool{}
			}
		}
		// Requests in flight were made before the change and must not be stored.
		x.epochs[key]++
		x.store.Set(key, e)
	}
	x.mu.Unlock()
	if list && a.OnMusicInvalidated != nil {
		a.OnMusicInvalidated(id)
	}
}
