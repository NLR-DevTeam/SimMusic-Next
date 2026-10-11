package core

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"simmusic/internal/audio"
	"simmusic/internal/config"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

func queueFixture(t *testing.T, mode int, names ...string) *App {
	t.Helper()
	dir := t.TempDir()
	a := &App{DataDir: dir, Cfg: config.Open(filepath.Join(dir, "config.json"), Defaults), State: config.Open(filepath.Join(dir, "state.json"), nil), Engine: &audio.Engine{}, index: -1, mode: mode, failCount: 1000}
	a.Plugins = plugin.NewManager(filepath.Join(dir, "plugin-data"), filepath.Join(dir, "plugins"), a.State, a)
	for _, name := range names {
		a.queue = append(a.queue, model.Track{ID: name, Title: name, Source: "missing"})
	}
	a.shuffle = newShuffle(len(names))
	t.Cleanup(func() { a.Cfg.Flush(); a.State.Flush() })
	return a
}

func assertOrder(t *testing.T, tracks []model.Track, names ...string) {
	t.Helper()
	got := make([]string, len(tracks))
	for i, track := range tracks {
		got[i] = track.Title
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("order = %v, want %v", got, names)
	}
}

// Both the operation and subsequent snapshots must return. The former shuffle
// loop held App.mu forever, so EOF and every following request failed this check.
func completeLoad(t *testing.T, a *App, action func()) {
	t.Helper()
	a.mu.Lock()
	before := a.failCount
	a.mu.Unlock()
	done := make(chan struct{})
	go func() { action(); a.Snapshot(); a.Sleep(); a.Mode(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue operation or subsequent core request hung")
	}
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		finished := a.failCount > before
		a.mu.Unlock()
		if finished {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background load did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestShuffleEndAndModeRestore(t *testing.T) {
	a := queueFixture(t, ModeShuffle, "A", "B", "C", "D")
	a.shuffle, a.index = []int{2, 0, 3, 1}, 2
	for _, want := range []string{"A", "D", "B", "C"} {
		completeLoad(t, a, a.onEnded)
		if got := a.Snapshot().Current.Title; got != want {
			t.Fatalf("EOF chose %q, want %q", got, want)
		}
	}
	completeLoad(t, a, a.Prev)
	if a.Snapshot().Current.Title != "B" {
		t.Fatal("previous must follow the same shuffle list backwards")
	}
	before := a.loadGen
	a.CycleMode()
	s := a.Snapshot()
	assertOrder(t, s.Queue, "A", "B", "C", "D")
	if s.Index != 1 || s.Current.Title != "B" || a.loadGen != before {
		t.Fatal("leaving shuffle must retain the exact current occurrence without reloading")
	}
	completeLoad(t, a, func() { a.Next(false) })
	if a.Snapshot().Current.Title != "C" {
		t.Fatal("normal next must resume the original order")
	}
	a.CycleMode()
	a.CycleMode()
	assertOrder(t, a.Snapshot().Queue, "C", "A", "D", "B")
}

func song(name string) model.Track { return model.Track{ID: name, Title: name, Source: "missing"} }

func TestShuffleMutationsKeepSongsUnique(t *testing.T) {
	a := queueFixture(t, ModeShuffle, "A", "B", "C", "D")
	a.shuffle, a.index = []int{2, 0, 3, 1}, 1
	a.PlayNextMany([]model.Track{song("E"), song("F"), song("E")})
	assertOrder(t, a.queue, "A", "B", "E", "F", "C", "D")
	assertOrder(t, a.Snapshot().Queue, "C", "A", "D", "B", "E", "F")
	// Songs already queued are not added again; the new one goes to the end of both orders.
	a.AppendQueue([]model.Track{song("B"), song("G"), song("G"), song("C")})
	assertOrder(t, a.queue, "A", "B", "E", "F", "C", "D", "G")
	if !validShuffle(a.shuffle, len(a.queue)) {
		t.Fatal("append broke the shuffle permutation")
	}
	// "Play next" on queued songs moves them behind the current one; the current song stays put.
	a.PlayNextMany([]model.Track{song("D"), song("B"), song("A")})
	assertOrder(t, a.queue, "B", "D", "A", "E", "F", "C", "G")
	assertOrder(t, a.Snapshot().Queue, "C", "B", "D", "A", "E", "F", "G")
	if a.Snapshot().Current.Title != "B" || !validShuffle(a.shuffle, len(a.queue)) {
		t.Fatal("moving queued songs changed the current song or the permutation")
	}
	a.RemoveFromQueue(0)
	assertOrder(t, a.queue, "B", "D", "A", "E", "F", "G")
	if a.Snapshot().Current.Title != "B" || a.index != 0 {
		t.Fatal("removing an earlier song changed the current song")
	}
	completeLoad(t, a, func() { a.RemoveFromQueue(a.Snapshot().Index) })
	assertOrder(t, a.queue, "D", "A", "E", "F", "G")
	if a.Snapshot().Current.Title != "D" {
		t.Fatal("removing current must play the next shuffled song")
	}
	if !validShuffle(a.shuffle, len(a.queue)) {
		t.Fatal("remove broke the shuffle permutation")
	}
	a.CycleMode()
	assertOrder(t, a.Snapshot().Queue, "D", "A", "E", "F", "G")
	a.ClearQueue()
	if len(a.queue) != 0 || len(a.shuffle) != 0 || a.HasCurrent() {
		t.Fatal("clear must empty both lists")
	}
}

func TestShufflePersistenceAndLegacyRestore(t *testing.T) {
	a := queueFixture(t, ModeShuffle, "A", "B", "C")
	a.shuffle, a.index = []int{2, 0, 1}, 2
	a.saveQueue()
	a.State.Set("mode", ModeShuffle)
	a.State.Flush()
	b := &App{State: config.Open(filepath.Join(a.DataDir, "state.json"), nil), Engine: &audio.Engine{}, mode: ModeShuffle}
	b.RestoreQueue()
	if !reflect.DeepEqual(b.shuffle, a.shuffle) || b.index != 2 {
		t.Fatal("restart must preserve shuffle order and the current song")
	}
	b.CycleMode()
	assertOrder(t, b.Snapshot().Queue, "A", "B", "C")
	if b.index != 2 {
		t.Fatal("normal restore selected the wrong song")
	}
	b.State.Delete("shuffleOrder")
	b.RestoreQueue()
	if !validShuffle(b.shuffle, 3) {
		t.Fatal("legacy state must build a complete shuffle list")
	}
	b.State.Set("shuffleOrder", []int{0, 0, 9})
	b.RestoreQueue()
	if !validShuffle(b.shuffle, 3) {
		t.Fatal("invalid saved order must be rebuilt")
	}
	// A queue saved by an older version may repeat a song: one occurrence survives, the current song stays current.
	b.State.Set("queue", []model.Track{song("A"), song("B"), song("A"), song("B"), song("C")})
	b.State.Set("index", 3)
	b.RestoreQueue()
	assertOrder(t, b.queue, "A", "B", "C")
	if b.index != 1 || !validShuffle(b.shuffle, 3) {
		t.Fatal("legacy duplicates must collapse onto the current song")
	}
	b.State.Flush()
}

func TestPlayQueueDropsRepeatedSongs(t *testing.T) {
	a := queueFixture(t, ModeLoop)
	completeLoad(t, a, func() { a.PlayQueue([]model.Track{song("A"), song("B"), song("A"), song("C"), song("B")}, 4) })
	assertOrder(t, a.queue, "A", "B", "C")
	if a.Snapshot().Current.Title != "B" {
		t.Fatal("the requested song must stay selected after duplicates are dropped")
	}
}

// While the song switched to is still opening, the previous one is silent and the player reports the new one.
func TestSwitchingReportsTheNewSong(t *testing.T) {
	a := queueFixture(t, ModeLoop, "A", "B")
	a.mu.Lock()
	a.index, a.loadGen = 1, 5
	a.loading, a.switching, a.wantPlay, a.loadPos = 5, 5, true, 0
	a.queue[1].Duration = 200
	a.mu.Unlock()
	s := a.Snapshot()
	if !s.Loading || !s.Playing || s.Pos != 0 || s.Dur != 200 || s.Current.Title != "B" {
		t.Fatalf("a switch in flight must show the new song from its start: %+v", s)
	}
	a.TogglePlay()
	if a.Snapshot().Playing || a.Engine.Playing() {
		t.Fatal("pause pressed while loading must cancel the pending playback, not resume the previous song")
	}
}

func TestPlayQueueModes(t *testing.T) {
	for _, mode := range []int{ModeLoop, ModeRepeat, ModeShuffle} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			a := queueFixture(t, mode)
			tracks := []model.Track{song("A"), song("B"), song("C")}
			completeLoad(t, a, func() { a.PlayQueue(tracks, 1) })
			if a.Snapshot().Current.Title != "B" || !validShuffle(a.shuffle, 3) {
				t.Fatal("starting a queue must select the requested normal-list occurrence")
			}
			before := a.Snapshot().Index
			completeLoad(t, a, a.onEnded)
			if mode == ModeRepeat && a.Snapshot().Index != before {
				t.Fatal("single-track repeat moved to another song")
			}
			if mode != ModeRepeat && a.Snapshot().Index != (before+1)%3 {
				t.Fatal("next did not follow the active order")
			}
			completeLoad(t, a, func() { a.PlayQueue(tracks[:1], -1) })
			completeLoad(t, a, a.onEnded)
			if a.Snapshot().Current.Title != "A" {
				t.Fatal("one-song EOF must remain responsive")
			}
			a.ClearQueue()
			a.onEnded()
			a.Next(false)
			a.Prev()
			if a.HasCurrent() {
				t.Fatal("empty queue should remain empty")
			}
		})
	}
}

func TestStoppedQueueStartsAtFirstActivePosition(t *testing.T) {
	for _, mode := range []int{ModeLoop, ModeRepeat, ModeShuffle} {
		a := queueFixture(t, mode, "A", "B", "C")
		completeLoad(t, a, func() { a.Next(false) })
		if a.Snapshot().Index != 0 {
			t.Fatal("next on a stopped queue must select the first active position")
		}
		a.Stop()
		completeLoad(t, a, a.onEnded)
		if a.Snapshot().Index != 0 {
			t.Fatal("EOF with no current position must not panic or skip the first song")
		}
		before := a.Snapshot()
		modified := a.Snapshot()
		modified.Queue[0].Title = "changed by caller"
		if !reflect.DeepEqual(before.Queue, a.Snapshot().Queue) {
			t.Fatal("snapshot must not expose mutable queue storage")
		}
	}
}

func TestMoveQueueKeepsCurrentTrackAndShuffleTargets(t *testing.T) {
	dir := t.TempDir()
	tracks := func(ids ...string) (out []model.Track) {
		for _, id := range ids {
			out = append(out, model.Track{Source: "test", ID: id})
		}
		return
	}
	ids := func(ts []model.Track) (out string) {
		for _, track := range ts {
			out += track.ID
		}
		return
	}
	a := &App{State: config.Open(filepath.Join(dir, "state.json"), nil), queue: tracks("a", "b", "c", "d"), shuffle: []int{2, 0, 3, 1}, index: 1}
	a.MoveQueue(1, 3)
	if ids(a.queue) != "acdb" || a.queue[a.index].ID != "b" {
		t.Fatalf("normal move: %s current %d", ids(a.queue), a.index)
	}
	a.mode = ModeShuffle
	if ids(a.activeQueueLocked()) != "cadb" {
		t.Fatalf("shuffle order must keep pointing at the same tracks: %s", ids(a.activeQueueLocked()))
	}
	a.MoveQueue(0, 2)
	if ids(a.activeQueueLocked()) != "adcb" || ids(a.queue) != "acdb" || a.queue[a.index].ID != "b" {
		t.Fatalf("shuffle move: %s / %s", ids(a.activeQueueLocked()), ids(a.queue))
	}
	a.MoveQueue(0, 9)
	if ids(a.activeQueueLocked()) != "adcb" {
		t.Fatal("out-of-range move changed the queue")
	}
}
