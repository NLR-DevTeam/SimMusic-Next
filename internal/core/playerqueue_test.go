package core

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

// A queue set around the current song leaves it playing as it is, and the random order of the songs kept.
func TestSetQueueKeepsTheCurrentSong(t *testing.T) {
	a := queueFixture(t, ModeShuffle, "A", "B", "C")
	a.mu.Lock()
	a.index, a.shuffle = 1, []int{2, 0, 1}
	gen := a.loadGen
	a.mu.Unlock()
	a.SetQueue([]model.Track{song("B"), song("D"), song("C"), song("E")}, -1, nil, nil)
	a.mu.Lock()
	reopened, current := a.loadGen != gen, a.queue[a.index].Title
	order := a.activeQueueLocked()
	valid := validShuffle(a.shuffle, len(a.queue))
	a.mu.Unlock()
	if reopened || current != "B" {
		t.Fatalf("the current song was replaced or reopened: %s %v", current, reopened)
	}
	assertOrder(t, a.queue, "B", "D", "C", "E")
	if !valid || order[0].Title != "C" || order[1].Title != "B" {
		t.Fatalf("songs kept lost their random order: %v", order)
	}
	position, play := 30.0, false
	completeLoad(t, a, func() { a.SetQueue([]model.Track{song("D"), song("E")}, 1, &position, &play) })
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queue[a.index].Title != "E" || a.wantPlay || a.loadPos != 30 {
		t.Fatalf("a new current song must open where and as asked: %s %v %v", a.queue[a.index].Title, a.wantPlay, a.loadPos)
	}
}

func TestPlayerQueueAndPlayback(t *testing.T) {
	a := queueFixture(t, ModeLoop)
	t.Cleanup(a.Plugins.StopAll)
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
	add(plugin.Manifest{ID: "online", Version: "1", Permissions: []string{plugin.PermNetwork}, Schemes: []plugin.SchemeDeclaration{{Scheme: "alpha"}}},
		`exports.getMusicInfo = (ids, scene) => ids.map(id => ({ id, title: "Song " + id, artist: "", album: "", duration: 5, cover: null,
			playback: scene === "play" ? { url: "https://cdn.example.com/" + id, headers: { Cookie: "k=v" }, expire: 60 } : undefined }));`)
	sync := add(plugin.Manifest{ID: "sync", Version: "1", Permissions: []string{plugin.PermPlayer}, Schemes: []plugin.SchemeDeclaration{{Scheme: "sync"}}},
		`exports.getMusicInfo = (ids, scene) => ids.map(id => ({ id, title: "Shared " + id, artist: "", album: "", duration: 5, cover: null,
			playback: scene === "play" ? new File(["audio"], id + ".wav") : undefined }));`)
	a.mu.Lock()
	a.queue, a.index, a.shuffle = []model.Track{{Source: "alpha", ID: "1", Title: "Song 1"}}, 0, []int{0}
	a.mu.Unlock()

	// Its own songs and those the queue holds may be queued; another extension's other songs may not.
	if err := a.PlayerQueue(sync, "set", []model.Track{{Source: "alpha", ID: "2"}}, plugin.QueueOptions{Index: -1}); err == nil {
		t.Fatal("a song of another protocol the queue does not hold was queued")
	}
	if err := a.PlayerQueue(sync, "set", []model.Track{{ID: "x"}, {Source: "alpha", ID: "1"}}, plugin.QueueOptions{Index: -1}); err != nil {
		t.Fatal(err)
	}
	s := a.Snapshot()
	if len(s.Queue) != 2 || s.Queue[0].Source != "sync" || s.Queue[0].InfoReady || s.Current.ID != "1" {
		t.Fatalf("queue: %+v", s.Queue)
	}
	a.PlayerLoadInfo([]int{0})
	until := time.Now().Add(time.Second)
	for !a.Snapshot().Queue[0].InfoReady && time.Now().Before(until) {
		time.Sleep(5 * time.Millisecond)
	}
	if a.Snapshot().Queue[0].Title != "Shared x" {
		t.Fatal("visible queue metadata was not loaded")
	}
	if err := a.PlayerQueue(sync, "append", []model.Track{{ID: "y"}}, plugin.QueueOptions{Index: -1}); err != nil || len(a.Snapshot().Queue) != 3 {
		t.Fatal("append", err)
	}

	// An address goes with its headers; a file as its path.
	r, err := a.PlayerPlayback("alpha", "1")
	if err != nil || r.URL != "https://cdn.example.com/1" || r.Headers["Cookie"] != "k=v" || r.Path != "" || r.Expire != 0 || r.ExpiresAt != 0 || r.Provider != "" {
		t.Fatalf("address: %+v %v", r, err)
	}
	r, err = a.PlayerPlayback("sync", "x")
	if data, _ := os.ReadFile(r.Path); err != nil || string(data) != "audio" || r.URL != "" {
		t.Fatalf("file: %+v %v", r, err)
	}
	if _, err := a.PlayerPlayback("alpha", "9"); err == nil || !strings.Contains(err.Error(), "队列") {
		t.Fatal("a song outside the queue was handed over", err)
	}
}

func TestPlayerQueueDoesNotFetchTheWholeListBeforeSwitching(t *testing.T) {
	a, p := musicFixture(t, `let calls=0; exports.calls=()=>calls; exports.getMusicInfo=()=>{calls++;throw new Error("metadata must be lazy")};`, nil)
	a.mu.Lock()
	a.queue, a.index, a.shuffle = []model.Track{{Source: "test", ID: "current", Title: "Current"}}, 0, []int{0}
	a.mu.Unlock()
	refs := []model.Track{{Source: "test", ID: "next"}, {Source: "test", ID: "current"}}
	if err := a.PlayerQueue(p, "set", refs, plugin.QueueOptions{Index: -1}); err != nil {
		t.Fatal(err)
	}
	var calls int
	if err := p.CallInto(time.Second, &calls, "calls"); err != nil || calls != 0 {
		t.Fatalf("queue blocked on list metadata: %d %v", calls, err)
	}
}
