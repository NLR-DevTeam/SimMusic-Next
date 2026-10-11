package core

import (
	"testing"
	"time"

	"simmusic/internal/model"
)

// Plain data stays in the index through everything short of the user's own clear: an expired cover,
// an invalidation by the extension, a failing extension. It changes only when a request returns it anew.
func TestIndexKeepsPlainData(t *testing.T) {
	a, p := musicFixture(t, `let revision = 0; let broken = false;
exports.bump = () => ++revision; exports.breakIt = value => broken = value;
exports.getMusicInfo = async (ids, scene) => {
 if (broken) throw new Error("offline");
 return ids.map(id => scene === "list" ? {id, title: "Song " + revision, artist: "Artist", album: "Album", duration: 60, cover: {url: "https://example.com/cover", expire: .1}} : {id, playback: {url: "https://example.com/audio", expire: 60}, lyrics: "[00:00]Lyric " + revision});
};`, nil)
	ref := []model.Track{{Source: "test", ID: "a"}}
	if _, _, err := a.resolveMusicInfo(ref[0], "play"); err != nil {
		t.Fatal(err)
	}
	p.Call(time.Second, "bump")
	time.Sleep(150 * time.Millisecond)

	// The cover has expired. The extension cannot be reached: everything else is still served.
	p.Call(time.Second, "breakIt", true)
	if cached := a.cachedTrackRefs([]model.Track{{Source: "test", ID: "a"}}); cached[0].Title != "Song 0" || cached[0].Duration != 60 || cached[0].InfoReady {
		t.Fatalf("an expired cover must leave the text in place and the record incomplete: %+v", cached[0])
	}
	stale, err := a.GetMusicInfo(ref, "list")
	if err != nil || stale[0].Title != "Song 0" || stale[0].Artist != "Artist" || stale[0].Cover != "" {
		t.Fatalf("a failed refresh must fall back to the index: %+v %v", stale, err)
	}
	if a.cachedLyrics(ref[0]) != "[00:00]Lyric 0" {
		t.Fatal("lyrics text must stay readable")
	}

	// The extension invalidates its music, as it does when a setting changes: resources go, text stays.
	a.ClearPluginMusicInfo("test")
	if cached := a.cachedTrackRefs([]model.Track{{Source: "test", ID: "a"}}); cached[0].Title != "Song 0" || a.cachedLyrics(ref[0]) != "[00:00]Lyric 0" {
		t.Fatalf("an invalidation must keep plain data: %+v", cached[0])
	}

	// Once the extension answers again, the refreshed request brings the new text along.
	p.Call(time.Second, "breakIt", false)
	fresh, err := a.GetMusicInfo(ref, "list")
	if err != nil || fresh[0].Title != "Song 1" || fresh[0].Cover == "" {
		t.Fatalf("a successful refresh updates the record: %+v %v", fresh, err)
	}
	if _, _, err := a.resolveMusicInfo(ref[0], "play"); err != nil || a.cachedLyrics(ref[0]) != "[00:00]Lyric 1" {
		t.Fatalf("the play request must have been made again after the invalidation: %v", err)
	}

	// The extension drops one field of its own songs (sim.index.clear), as after a quality change:
	// the playback address goes and the play scene is asked again, the rest of the record is untouched.
	p.Call(time.Second, "bump")
	a.ClearPluginFields("other", []string{"title", "playback"}, nil)
	a.ClearPluginFields("test", []string{"playback"}, []string{"someone-else"})
	if info, _, _ := a.musicInfos(ref, "play"); info[0].Title != "Song 1" || string(info[0].Lyrics) != `"[00:00]Lyric 1"` {
		t.Fatalf("songs of other extensions or other ids must be left alone: %+v", info[0])
	}
	a.ClearPluginFields("test", []string{"playback"}, nil)
	if cached := a.cachedTrackRefs([]model.Track{{Source: "test", ID: "a"}}); cached[0].Title != "Song 1" || !cached[0].InfoReady {
		t.Fatalf("clearing the playback address must not touch the list data: %+v", cached[0])
	}
	if info, _, err := a.musicInfos(ref, "play"); err != nil || string(info[0].Lyrics) != `"[00:00]Lyric 2"` || info[0].Title != "Song 1" {
		t.Fatalf("the play scene must be requested again, alone: %+v %v", info, err)
	}

	// Only the user's clear drops the record.
	a.ClearAllMusicInfo()
	if cached := a.cachedTrackRefs([]model.Track{{Source: "test", ID: "a"}}); cached[0].Title != "" {
		t.Fatal("the user's clear must remove plain data too")
	}
}
