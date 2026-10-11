package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"simmusic/internal/config"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func musicFixture(t *testing.T, script string, menus []model.Action) (*App, *plugin.Plugin) {
	t.Helper()
	dir := t.TempDir()
	a := &App{DataDir: dir, State: config.Open(filepath.Join(dir, "state.json"), nil)}
	a.Plugins = plugin.NewManager(filepath.Join(dir, "plugin-data"), filepath.Join(dir, "plugins"), a.State, nil)
	manifest := plugin.Manifest{ID: "test", Version: "1", Permissions: []string{"network", "trackMenu", "shell"}, TrackMenus: menus}
	yes := true
	manifest.Capabilities.Download = &yes
	b, _ := json.Marshal(manifest)
	if err := a.Plugins.AddBuiltin(fstest.MapFS{"plugin.json": {Data: b}, "main.js": {Data: []byte(script)}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Plugins.StopAll)
	return a, a.Plugins.Get("test")
}

const musicScript = `let calls = []; let revision = 0;
exports.calls = () => calls;
exports.bump = () => ++revision;
exports.getMusicInfo = async (ids, scene) => {
 calls.push({ids, scene}); await new Promise(resolve => setTimeout(resolve, 35));
 return ids.map(id => scene === "list" ? {id, title: "Song " + revision, artist: "Artist", album: "Album", duration: 60, cover: id === "volatile" ? {url: "https://example.com/volatile"} : id === "ttl" ? {url: "https://example.com/ttl", expire: .12} : new File(["image"], "cover.png"), extra: {rating: 5}} : scene === "download" ? {id, ext:"wav"} : {id, playback: new File(["audio"], "song.wav"), lyrics: "[00:00]Lyric"});
};`

func TestMusicInfoBatchDedupAndSceneFallbacks(t *testing.T) {
	a, p := musicFixture(t, musicScript, nil)
	refs := []model.Track{{Source: "test", ID: "a"}, {Source: "test", ID: "b"}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ts, err := a.GetMusicInfo(refs, "list")
			if err != nil || len(ts) != 2 || ts[0].Title != "Song 0" {
				t.Errorf("batch: %v %v", ts, err)
			}
		})
	}
	wg.Wait()
	var calls []struct {
		IDs   []string `json:"ids"`
		Scene string   `json:"scene"`
	}
	if err := p.CallInto(time.Second, &calls, "calls"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0].IDs) != 2 {
		t.Fatalf("overlapping requests must share one batch: %+v", calls)
	}
	info, track, err := a.resolveMusicInfo(refs[0], "download")
	if err != nil || info.Download.Path == "" || track.LargeCover != track.Cover || a.musicLyrics("test", info.Lyrics) != "[00:00]Lyric" {
		t.Fatalf("scene fallback: %+v %+v %v", info, track, err)
	}
	if b, err := os.ReadFile(info.Download.Path); err != nil || string(b) != "audio" {
		t.Fatalf("File materialization: %q %v", b, err)
	}
}

func TestDownloadMenuGrant(t *testing.T) {
	a, p := musicFixture(t, musicScript, nil)
	track := model.Track{Source: "test", ID: "a"}
	if !a.CanDownload(track) {
		t.Fatal("declared download capability must be usable")
	}
	if err := p.SetTrackMenuGranted("download", false); err != nil {
		t.Fatal(err)
	}
	if a.CanDownload(track) {
		t.Fatal("download menu revocation did not reach the application")
	}
	if _, _, err := a.musicInfos([]model.Track{track}, "download"); err == nil {
		t.Fatal("direct scene call bypassed revocation")
	}
	if _, err := p.Call(time.Second, "getMusicInfo", []string{"a"}, "download"); err == nil {
		t.Fatal("Goja callback bypassed revocation")
	}
}

func TestMusicInfoExpirationPersistenceAndClear(t *testing.T) {
	a, p := musicFixture(t, musicScript, nil)
	refs := []model.Track{{Source: "test", ID: "ttl"}, {Source: "test", ID: "volatile"}}
	if _, err := a.GetMusicInfo(refs, "list"); err != nil {
		t.Fatal(err)
	}
	if err := a.FlushMusicIndex(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(a.DataDir, "music-index.json"))
	if strings.Contains(string(b), "https://example.com/volatile") || !strings.Contains(string(b), "https://example.com/ttl") || !strings.Contains(string(b), "rating") {
		t.Fatalf("bounded resource persistence: %s", b)
	}
	time.Sleep(160 * time.Millisecond)
	if _, err := a.GetMusicInfo(refs[:1], "list"); err != nil {
		t.Fatal(err)
	}
	var calls []any
	p.CallInto(time.Second, &calls, "calls")
	if len(calls) != 2 {
		t.Fatalf("expired URL must be refreshed: %d", len(calls))
	}
	p.Call(time.Second, "bump")
	if cached, err := a.GetMusicInfo(refs[:1], "list"); err != nil || cached[0].Title != "Song 0" {
		t.Fatalf("plain metadata must stay indexed until the user clears it: %v %v", cached, err)
	}
	fresh, err := a.ClearMusicInfo(refs[:1], "list")
	if err != nil || fresh[0].Title != "Song 1" {
		t.Fatalf("clear must drop the host index: %v %v", fresh, err)
	}
}

func TestMusicInfoClearDuringFlight(t *testing.T) {
	a, p := musicFixture(t, musicScript, nil)
	ref := []model.Track{{Source: "test", ID: "a"}}
	result := make(chan error, 1)
	go func() { _, err := a.GetMusicInfo(ref, "list"); result <- err }()
	for deadline := time.Now().Add(time.Second); ; {
		var calls []any
		p.CallInto(time.Second, &calls, "calls")
		if len(calls) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request did not start")
		}
		time.Sleep(time.Millisecond)
	}
	p.Call(time.Second, "bump")
	fresh, err := a.ClearMusicInfo(ref, "list")
	if err != nil || fresh[0].Title != "Song 1" {
		t.Fatalf("refresh: %v %v", fresh, err)
	}
	if err := <-result; err == nil {
		t.Fatal("a pre-clear request must not repopulate the index")
	}
	if cached, err := a.GetMusicInfo(ref, "list"); err != nil || cached[0].Title != "Song 1" {
		t.Fatalf("stale record resurrected: %v %v", cached, err)
	}
}

func TestSongMenuSceneGrantsAndExecution(t *testing.T) {
	menus := []model.Action{{Key: "reveal", Label: "Reveal", Scenes: []string{"list"}, Permissions: []string{"shell"}, Selection: "single"}, {Key: "quality", Label: "Quality", Scenes: []string{"play"}, Dynamic: true}}
	a, p := musicFixture(t, `exports.getMusicInfo = () => []; exports.getTrackActions = () => [{key:"lossless",label:"Lossless",icon:"EF83"}]; exports.onTrackAction = (key, tracks, scene) => sim.settings.set("last", {key,scene});`, menus)
	refs := []model.Track{{Source: "test", ID: "a"}}
	if err := a.ExecuteSongAction(refs, "reveal", "play"); err == nil {
		t.Fatal("scene mismatch accepted")
	}
	if err := a.ExecuteSongAction(refs, "forged", "list"); err == nil {
		t.Fatal("undeclared menu accepted")
	}
	if err := a.ExecuteSongAction(refs, "quality/lossless", "play"); err != nil {
		t.Fatal(err)
	}
	p.SetTrackMenuGranted("quality", false)
	if err := a.ExecuteSongAction(refs, "quality/lossless", "play"); err == nil {
		t.Fatal("revoked ancestor accepted")
	}
	a.Plugins.SetGranted(p.ID(), plugin.PermShell, false)
	if err := a.ExecuteSongAction(refs, "reveal", "list"); err == nil {
		t.Fatal("revoked API permission accepted")
	}
	a.Plugins.SetGranted(p.ID(), plugin.PermTrackMenu, false)
	if _, err := p.Call(time.Second, "onTrackAction", "reveal", refs, "list"); err == nil {
		t.Fatal("master grant bypassed through VM callback")
	}
}
