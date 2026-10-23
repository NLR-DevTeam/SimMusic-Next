package core

import (
	"errors"

	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

// PlayerTrackMenu exposes the same menu used by host song lists. Input metadata is ignored;
// only songs registered by the requesting extension and selected from this list are accepted.
func (a *App) PlayerTrackMenu(p *plugin.Plugin, refs, selected []model.Track) error {
	if !p.HasPermission(plugin.PermPlayer) {
		return errors.New("权限被拒绝：播放控制未授权")
	}
	if len(refs) == 0 || len(refs) > 50000 || len(selected) == 0 || len(selected) > len(refs) {
		return errors.New("无效的歌曲列表或选择")
	}
	clean := func(values []model.Track) ([]model.Track, error) {
		out := make([]model.Track, len(values))
		for i, ref := range values {
			if ref.ID == "" {
				return nil, errors.New("歌曲缺少 id")
			}
			out[i] = model.Track{Source: ref.Source, ID: ref.ID}
		}
		return out, a.Plugins.ValidateTracks(p, out)
	}
	list, err := clean(refs)
	if err != nil {
		return err
	}
	selection, err := clean(selected)
	if err != nil {
		return err
	}
	list, _ = uniqueTracks(list, 0)
	// The menu only needs metadata for the selected songs. Hydrating a whole large list
	// here used to deserialize every indexed record before the native menu could open.
	selection = a.cachedTrackRefs(selection)
	known := map[string]model.Track{}
	for _, track := range list {
		known[track.Key()] = track
	}
	seen := map[string]bool{}
	var chosen []model.Track
	for _, ref := range selection {
		_, ok := known[ref.Key()]
		if !ok {
			return errors.New("选中的歌曲不在提供的列表中")
		}
		if !seen[ref.Key()] {
			chosen = append(chosen, ref)
			seen[ref.Key()] = true
		}
	}
	if a.OnTrackMenu != nil {
		a.OnTrackMenu(list, chosen)
	}
	return nil
}
