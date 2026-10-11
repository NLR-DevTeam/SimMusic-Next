package core

import (
	"path/filepath"
	"strings"
	"time"
)

// ReleaseFile makes the player let go of a local file an extension is about to replace (its
// metadata is being rewritten). The returned function reopens the song where it was and waits for it.
func (a *App) ReleaseFile(path string) func() {
	a.mu.Lock()
	i := a.index
	current := i >= 0 && i < len(a.queue) && strings.EqualFold(filepath.Clean(a.queue[i].ID), filepath.Clean(path))
	a.mu.Unlock()
	if !current || !a.Engine.Loaded() {
		return func() {}
	}
	playing := a.Engine.Playing()
	position, _ := a.Engine.Position()
	a.Engine.Unload()
	return func() {
		a.startTrackAt(i, playing, position, true)
		a.mu.Lock()
		gen := a.loadGen
		a.mu.Unlock()
		// The song is open again before the caller goes on: what an extension does next (it
		// usually invalidates the song's information) must not find the reload still in flight
		// and cancel it.
		for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			a.mu.Lock()
			done := a.loading != gen || a.loadGen != gen
			a.mu.Unlock()
			if done {
				break
			}
		}
	}
}
