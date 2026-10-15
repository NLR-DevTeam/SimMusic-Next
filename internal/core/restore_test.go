package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/audio"
	"simmusic/internal/config"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

func TestRestoreLyricsFromPersistentIndexAndPlugin(t *testing.T) {
	a, _ := musicFixture(t, musicScript, nil)
	ref := model.Track{Source: "test", ID: "restore"}
	if _, _, err := a.resolveMusicInfo(ref, "play"); err != nil {
		t.Fatal(err)
	}
	a.State.Set("queue", []model.Track{ref})
	a.State.Set("index", 0)
	a.State.Set("position", 12.5)
	a.State.Flush()
	a.FlushMusicIndex()
	if index, _ := os.ReadFile(filepath.Join(a.DataDir, "music-index.json")); !strings.Contains(string(index), "Lyric") || strings.Contains(string(index), "resource-files") {
		t.Fatalf("the index keeps plain data and never a File: %s", index)
	}
	makeRestored := func() (*App, *plugin.Plugin) {
		b := &App{DataDir: a.DataDir, State: config.Open(filepath.Join(a.DataDir, "state.json"), nil), Cfg: config.Open(filepath.Join(a.DataDir, "config.json"), Defaults), Engine: &audio.Engine{}}
		b.Plugins = plugin.NewManager(filepath.Join(a.DataDir, "plugin-data"), filepath.Join(a.DataDir, "plugins"), b.State, nil)
		man := plugin.Manifest{ID: "test", Version: "1", Schemes: []plugin.SchemeDeclaration{{Scheme: "test"}}}
		data, _ := json.Marshal(man)
		if err := b.Plugins.AddBuiltin(fstest.MapFS{"plugin.json": {Data: data}, "main.js": {Data: []byte(musicScript)}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(b.Plugins.StopAll)
		b.RestoreQueue()
		for deadline := time.Now().Add(2 * time.Second); ; {
			b.mu.Lock()
			ready := len(b.lyricLns) > 0
			b.mu.Unlock()
			if ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("restored current track has no lyrics")
			}
			time.Sleep(time.Millisecond)
		}
		if b.Engine.Loaded() || b.Engine.Playing() || b.Snapshot().Pos != 12.5 || b.Snapshot().Current.Title != "Song 0" {
			t.Fatal("restore must recover metadata and position, paused; this fixture has no decodable audio")
		}
		return b, b.Plugins.Get("test")
	}
	b, p := makeRestored()
	var calls []any
	p.CallInto(time.Second, &calls, "calls")
	if len(calls) != 2 {
		t.Fatalf("File resources must come from the extension again: %d", len(calls))
	}
	b.SavePosition()
	if b.State.Float("position") != 12.5 {
		t.Fatal("unloaded engine overwrote saved position")
	}
	b.mu.Lock()
	b.loadGen++
	b.mu.Unlock()
	x := b.musicIndex()
	x.mu.Lock()
	delete(x.entries, ref.Key())
	x.store.Delete(ref.Key())
	x.mu.Unlock()
	b.FlushMusicIndex()
	_, p = makeRestored()
	p.CallInto(time.Second, &calls, "calls")
	if len(calls) != 2 {
		t.Fatalf("missing index must request list and play: %d", len(calls))
	}
}

func TestValidateManualEqualizerJSON(t *testing.T) {
	for _, value := range []any{"wrong", map[string]any{}, []any{map[string]any{"F": 1000, "Q": 1}}, []any{map[string]any{"F": 1000, "G": 1, "Q": 0}}} {
		if ValidateEQSetting("eq.filters", value) == nil {
			t.Fatalf("invalid JSON accepted: %+v", value)
		}
	}
	if err := ValidateEQSetting("eq.filters", []audio.EQFilter{{F: 1000, G: 4, Q: 2}}); err != nil {
		t.Fatal(err)
	}
	if ValidateEQSetting("eq.profile", []string{"json"}) == nil {
		t.Fatal("invalid mode accepted")
	}
}
