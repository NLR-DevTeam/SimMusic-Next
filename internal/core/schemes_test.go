package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"simmusic/internal/config"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

func TestSchemeOverridesResourcesDownloadAndAppendedMenus(t *testing.T) {
	dir := t.TempDir()
	a := &App{DataDir: dir, State: config.Open(filepath.Join(dir, "state.json"), nil)}
	a.Plugins = plugin.NewManager(filepath.Join(dir, "plugin-data"), filepath.Join(dir, "plugins"), a.State, nil)
	t.Cleanup(a.Plugins.StopAll)
	no, yes := false, true
	add := func(man plugin.Manifest, script string) *plugin.Plugin {
		data, _ := json.Marshal(man)
		if err := a.Plugins.AddBuiltin(fstest.MapFS{"plugin.json": {Data: data}, "main.js": {Data: []byte(script)}}); err != nil {
			t.Fatal(err)
		}
		p := a.Plugins.Get(man.ID)
		if p.LoadErr() != nil {
			t.Fatal(p.LoadErr())
		}
		return p
	}
	owner := add(plugin.Manifest{ID: "owner", Version: "1", Permissions: []string{plugin.PermTrackMenu}, TrackMenus: []model.Action{{Key: "reveal", Label: "Reveal", Scenes: []string{"list"}}}, Schemes: []plugin.SchemeDeclaration{{Scheme: "alpha", Download: &no}, {Scheme: "beta", Download: &no}}}, `exports.getMusicInfo = (ids,scene,scheme) => ids.map(id => ({id,title:scheme,artist:"Base",album:"Album",duration:5,cover:null,playback:scene === "play" ? new File(["base"],"base.wav") : null,lyrics:"[00:00]Base"})); exports.onTrackAction = key => sim.settings.set("action", key);`)
	patch := add(plugin.Manifest{ID: "patch", Version: "1", Permissions: []string{plugin.PermSchemeExtend, plugin.PermTrackMenu}, SchemeExtensions: []plugin.SchemeDeclaration{{Scheme: "alpha", Download: &yes, Handlers: map[string]string{"getMusicInfo": "patchedInfo", "getDownload": "patchedDownload", "getTrackActions": "qualities", "onTrackAction": "choose"}, TrackMenus: []model.Action{{Key: "quality", Label: "音质", Scenes: []string{"play"}, Dynamic: true}}}}}, `exports.patchedInfo = (ids,scene,scheme) => ids.map(id => ({id,title:"Patched " + scheme,artist:"Patch",album:"Album",duration:10,cover:new File(["cover"],"cover.png"),playback:scene === "play" ? new File(["play"],"play.wav") : null,lyrics:"[00:00]Patch"})); exports.patchedDownload = () => new File(["download"],"download.wav"); exports.qualities = (tracks,scene,parent) => [{key:"lossless",label:parent}]; exports.choose = key => sim.settings.set("chosen",key);`)
	ref := model.Track{Source: "alpha", ID: "1"}
	info, track, err := a.resolveMusicInfo(ref, "download")
	if err != nil || track.Title != "Patched alpha" || info.Download.Provider != "patch" || !a.CanDownload(ref) {
		t.Fatalf("override dispatch: %+v %+v %v", info, track, err)
	}
	if data, err := os.ReadFile(info.Download.Path); err != nil || string(data) != "download" {
		t.Fatal("download handler did not override musicInfo")
	}
	if cover := a.CoverBytes(track); string(cover) != "cover" {
		t.Fatal("cover permissions were checked against the wrong extension")
	}
	top, err := a.SongActions([]model.Track{{Source: "beta", ID: "1"}}, "list", "")
	if err != nil || len(top) != 1 || top[0].Key != "scheme:beta/reveal" {
		t.Fatalf("protocol menu: %+v %v", top, err)
	}
	if err := a.ExecuteSongAction([]model.Track{{Source: "beta", ID: "1"}}, top[0].Key, "list"); err != nil || owner.Settings.String("action") != "reveal" {
		t.Fatal("granted top-level protocol menu was rejected", err)
	}
	menus, err := a.SongActions([]model.Track{ref}, "play", "")
	if err != nil || len(menus) != 1 || menus[0].Key != "patch::scheme:alpha/quality" {
		t.Fatalf("appended menu: %+v %v", menus, err)
	}
	children, err := a.SongActions([]model.Track{ref}, "play", menus[0].Key)
	if err != nil || len(children) != 1 || children[0].Label != "quality" {
		t.Fatalf("submenu handler: %+v %v", children, err)
	}
	if err := a.ExecuteSongAction([]model.Track{ref}, children[0].Key, "play"); err != nil || patch.Settings.String("chosen") != "quality/lossless" {
		t.Fatal("menu routed to wrong handler", err)
	}
	patch.SetTrackMenuGranted("scheme:alpha/quality", false)
	if a.ExecuteSongAction([]model.Track{ref}, children[0].Key, "play") == nil {
		t.Fatal("menu grant bypass")
	}
	patch.SetTrackMenuGranted("scheme:alpha/download", false)
	if a.CanDownload(ref) {
		t.Fatal("scoped download grant bypass")
	}
	a.Plugins.SetGranted("patch", plugin.PermSchemeExtend, false)
	ts, err := a.GetMusicInfo([]model.Track{ref}, "list")
	if err != nil || ts[0].Title != "alpha" || a.CanDownload(ref) {
		t.Fatal("revoked override remained in cache", err)
	}
	if a.resourcePlugin("alpha", "patch") != nil {
		t.Fatal("revoked extension still reads cached resources")
	}
	ts, err = a.GetMusicInfo([]model.Track{{Source: "beta", ID: "1"}}, "list")
	if err != nil || ts[0].Title != "beta" {
		t.Fatal("second scheme was incorrectly overridden", err)
	}
}
