package core

import (
	"encoding/json"
	"fmt"
	"simmusic/internal/model"
	"time"
)

// A one-use handoff, separate from the index. URLs without expire are never
// written to the index but can still be handed to the imminent next playback.
type playPrefetch struct {
	track      model.Track
	generation int
	token      string
	done       chan struct{}
	info       model.MusicInfo
	fresh      model.Track
	err        error
	fetched    time.Time
}

func (a *App) playbackToken(t model.Track) string {
	s := a.Plugins.Scheme(t.Source)
	if s == nil {
		return ""
	}
	token := s.Version()
	for _, b := range s.Bindings {
		token += fmt.Sprintf("/%p", b.Plugin)
	}
	x := a.musicIndex()
	x.mu.Lock()
	epoch := x.epochs[t.Key()]
	x.mu.Unlock()
	return fmt.Sprintf("%s/%d", token, epoch)
}

func (a *App) prefetchNext() {
	if !a.Engine.Playing() {
		return
	}
	position, duration := a.Engine.Position()
	if duration <= 0 || duration-position > 10 {
		return
	}
	a.mu.Lock()
	if len(a.queue) == 0 || a.index < 0 || a.sleep.WaitTrackEnd {
		a.mu.Unlock()
		return
	}
	i := a.activeIndexLocked()
	if a.mode != ModeRepeat {
		i = (i + 1) % len(a.queue)
	}
	t := a.queue[a.normalIndexLocked(i)]
	generation := a.loadGen
	a.mu.Unlock()
	token := a.playbackToken(t)
	if token == "" {
		return
	}
	a.mu.Lock()
	if generation != a.loadGen {
		a.mu.Unlock()
		return
	}
	if p := a.prefetch; p != nil && p.generation == generation && p.track.Key() == t.Key() && p.token == token {
		a.mu.Unlock()
		return
	}
	p := &playPrefetch{track: t, generation: generation, token: token, done: make(chan struct{})}
	a.prefetch = p
	a.mu.Unlock()
	go func() {
		defer close(p.done)
		p.info, p.fresh, p.err = a.resolveMusicInfo(t, "play")
		p.fetched = time.Now()
	}()
}

func (a *App) preparedMusicInfo(t model.Track, p *playPrefetch) (model.MusicInfo, model.Track, error) {
	if p != nil {
		<-p.done
		now := float64(time.Now().UnixMilli()) / 1000
		valid := true
		var lyricResource model.Resource
		_ = json.Unmarshal(p.info.Lyrics, &lyricResource)
		for _, resource := range []model.Resource{p.info.Playback, p.info.Cover, p.info.LargeCover, lyricResource} {
			if resource.URL != "" && resource.ExpiresAt > 0 && resource.ExpiresAt <= now {
				valid = false
			}
		}
		if p.err == nil && p.token == a.playbackToken(t) && valid && time.Since(p.fetched) < 30*time.Second {
			return p.info, p.fresh, nil
		}
	}
	return a.resolveMusicInfo(t, "play")
}
