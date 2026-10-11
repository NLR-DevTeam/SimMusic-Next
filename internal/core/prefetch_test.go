package core

import (
	"simmusic/internal/model"
	"testing"
	"time"
)

func TestPrefetchVolatilePlaybackIsOneUseAndNeverIndexed(t *testing.T) {
	a, p := musicFixture(t, `let calls = 0; exports.calls = () => calls;
exports.getMusicInfo = (ids, scene) => ids.map(id => scene === "list" ? {id,title:id,artist:"Artist",album:"Album",duration:60,cover:null} : (calls++, {id, playback: {url:"https://example.com/song?"+calls}, lyrics:"[00:00]test"}));`, nil)
	track := model.Track{Source: "test", ID: "next"}
	prefetch := &playPrefetch{track: track, token: a.playbackToken(track), done: make(chan struct{})}
	prefetch.info, prefetch.fresh, prefetch.err = a.resolveMusicInfo(track, "play")
	prefetch.fetched = time.Now()
	close(prefetch.done)
	info, _, err := a.preparedMusicInfo(track, prefetch)
	if err != nil || info.Playback.URL != "https://example.com/song?1" {
		t.Fatalf("prepared resource not reused: %+v %v", info, err)
	}
	var calls int
	p.CallInto(time.Second, &calls, "calls")
	if calls != 1 {
		t.Fatalf("duplicate play call: %d", calls)
	}
	info, _, err = a.preparedMusicInfo(track, nil)
	if err != nil || info.Playback.URL != "https://example.com/song?2" {
		t.Fatal("volatile URL was cached in index")
	}
	if _, err := a.ClearMusicInfo([]model.Track{track}, "list"); err != nil {
		t.Fatal(err)
	}
	info, _, err = a.preparedMusicInfo(track, prefetch)
	if err != nil || info.Playback.URL != "https://example.com/song?3" {
		t.Fatal("cache clearing did not invalidate prepared information")
	}
}

func TestPrefetchExpiredAddressIsRefetched(t *testing.T) {
	a, _ := musicFixture(t, `exports.getMusicInfo = (ids,scene) => ids.map(id => scene === "list" ? {id,title:id,artist:"Artist",album:"Album",duration:60,cover:null} : {id,playback:{url:"https://example.com/song",expire:.01}});`, nil)
	track := model.Track{Source: "test", ID: "next"}
	prefetch := &playPrefetch{track: track, token: a.playbackToken(track), done: make(chan struct{})}
	prefetch.info, prefetch.fresh, prefetch.err = a.resolveMusicInfo(track, "play")
	prefetch.fetched = time.Now()
	close(prefetch.done)
	time.Sleep(20 * time.Millisecond)
	info, _, err := a.preparedMusicInfo(track, prefetch)
	if err != nil || info.Playback.ExpiresAt <= prefetch.info.Playback.ExpiresAt {
		t.Fatalf("expired play URL was reused: %+v %v", info, err)
	}
}
