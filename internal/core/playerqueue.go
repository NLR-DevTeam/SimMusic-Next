package core

import (
	"errors"

	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

// PlayerQueue changes the queue for an extension with playback control: "set" replaces it (SetQueue),
// "append" adds to its end, "playNext" places songs after the current one, all without a notice. A song
// the queue holds already may be named whatever its protocol and keeps the record the host has of it;
// any other song must be of a protocol of the extension's own.
func (a *App) PlayerQueue(p *plugin.Plugin, op string, refs []model.Track, options plugin.QueueOptions) error {
	a.mu.Lock()
	queued := map[string]model.Track{}
	for _, t := range a.queue {
		queued[t.Key()] = t
	}
	a.mu.Unlock()
	tracks := make([]model.Track, len(refs))
	var fresh []int
	for i, ref := range refs {
		if ref.ID == "" {
			return errors.New("歌曲缺少 id")
		}
		if t, ok := queued[ref.Key()]; ok && ref.Source != "" {
			tracks[i] = t
			continue
		}
		own := []model.Track{{Source: ref.Source, ID: ref.ID}}
		if err := a.Plugins.ValidateTracks(p, own); err != nil {
			return err
		}
		if t, ok := queued[own[0].Key()]; ok {
			tracks[i] = t
			continue
		}
		tracks[i] = own[0]
		fresh = append(fresh, i)
	}
	// Queue changes must not wait for metadata for an entire remote list. The current song is
	// resolved by startTrackAt; visible queue rows request their missing metadata in the background.
	if len(fresh) > 0 {
		refs := make([]model.Track, len(fresh))
		for i, at := range fresh {
			refs[i] = tracks[at]
		}
		for i, known := range a.cachedTrackRefs(refs) {
			tracks[fresh[i]] = known
		}
	}
	switch op {
	case "set":
		if options.Index >= len(tracks) {
			return errors.New("队列位置超出范围")
		}
		a.SetQueue(tracks, options.Index, options.Position, options.Play)
	case "append":
		a.appendQueue(tracks)
	case "playNext":
		a.playNextMany(tracks)
	default:
		return errors.New("未知的队列操作：" + op)
	}
	return nil
}

// PlayerPlayback is the audio the host plays a song of the queue from, the current one for an empty source and
// id: a file or an address with its request headers, as the song's extension supplied it.
func (a *App) PlayerPlayback(source, id string) (model.Resource, error) {
	s := a.Snapshot()
	track := s.Current
	if source != "" || id != "" {
		track = nil
		for i := range s.Queue {
			if s.Queue[i].Source == source && s.Queue[i].ID == id {
				track = &s.Queue[i]
				break
			}
		}
	}
	if track == nil {
		return model.Resource{}, errors.New("歌曲不在播放队列中")
	}
	info, _, err := a.resolveMusicInfo(*track, "play")
	if err != nil {
		return model.Resource{}, err
	}
	r := info.Playback
	if r.Empty() {
		return model.Resource{}, errors.New("扩展未返回音频文件")
	}
	if p := a.resourcePlugin(track.Source, r.Provider); p == nil || p.ValidateResource(r) != nil {
		return model.Resource{}, errors.New("提供音频的扩展无权访问该音频")
	}
	return model.Resource{URL: r.URL, Headers: r.Headers, Path: r.Path}, nil
}
