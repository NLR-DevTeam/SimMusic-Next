package core

import (
	"math/rand/v2"
	"slices"

	"simmusic/internal/model"
)

func newShuffle(n int) []int { return rand.Perm(n) }

func validShuffle(order []int, n int) bool {
	if len(order) != n {
		return false
	}
	seen := make([]bool, n)
	for _, i := range order {
		if i < 0 || i >= n || seen[i] {
			return false
		}
		seen[i] = true
	}
	return true
}

// queue 和 index 始终指向正常列表；只有界面及播放导航使用当前模式的顺序。
// 以下方法由持有 a.mu 的调用者使用。
func (a *App) shuffleIndexLocked(normal int) int {
	for i, value := range a.shuffle {
		if value == normal {
			return i
		}
	}
	return -1
}

func (a *App) activeIndexLocked() int {
	if a.mode == ModeShuffle {
		return a.shuffleIndexLocked(a.index)
	}
	return a.index
}

func (a *App) normalIndexLocked(active int) int {
	if a.mode == ModeShuffle {
		return a.shuffle[active]
	}
	return active
}

func (a *App) activeQueueLocked() []model.Track {
	if a.mode != ModeShuffle {
		return append([]model.Track(nil), a.queue...)
	}
	tracks := make([]model.Track, len(a.shuffle))
	for i, normal := range a.shuffle {
		tracks[i] = a.queue[normal]
	}
	return tracks
}

// MoveQueue 把当前模式顺序中的第 from 首移到第 to 位。
// 随机模式只调整随机顺序；正常模式调整列表本身，并让随机顺序继续指向原来的曲目。
func (a *App) MoveQueue(from, to int) {
	a.mu.Lock()
	n := len(a.queue)
	if from < 0 || to < 0 || from >= n || to >= n || from == to {
		a.mu.Unlock()
		return
	}
	moved := func(i int) int {
		switch {
		case i == from:
			return to
		case from < i && i <= to:
			return i - 1
		case to <= i && i < from:
			return i + 1
		}
		return i
	}
	if a.mode == ModeShuffle {
		a.shuffle = moveItem(a.shuffle, from, to)
	} else {
		a.queue = moveItem(a.queue, from, to)
		for i, normal := range a.shuffle {
			a.shuffle[i] = moved(normal)
		}
		if a.index >= 0 {
			a.index = moved(a.index)
		}
	}
	a.mu.Unlock()
	a.saveQueue()
	a.redraw()
}

func moveItem[T any](items []T, from, to int) []T {
	out := append([]T(nil), items...)
	item := out[from]
	out = append(out[:from], out[from+1:]...)
	return append(out[:to], append([]T{item}, out[to:]...)...)
}

// SetQueue replaces the queue by tracks, in this order, and makes the song at index the current one; index -1
// is the current song when tracks hold it, the first otherwise. When that song is current already it goes on
// undisturbed, and position and play, when given, move it and start or pause it; a song that becomes current
// is opened at position, and played unless play is false.
func (a *App) SetQueue(tracks []model.Track, index int, position *float64, play *bool) {
	if len(tracks) == 0 {
		a.ClearQueue()
		return
	}
	a.mu.Lock()
	current := ""
	if a.index >= 0 && a.index < len(a.queue) {
		current = a.queue[a.index].Key()
	}
	if index < 0 {
		index = 0
		for i, t := range tracks {
			if t.Key() == current {
				index = i
				break
			}
		}
	}
	tracks, index = uniqueTracks(tracks, min(index, len(tracks)-1))
	a.shuffle = keptShuffle(a.queue, a.shuffle, tracks)
	a.queue = tracks
	if tracks[index].Key() == current {
		a.index = index
		a.mu.Unlock()
		if position != nil {
			a.Seek(max(0, *position))
		}
		if play != nil && a.Snapshot().Playing != *play {
			a.TogglePlay()
		}
		a.saveQueue()
		a.redraw()
		return
	}
	a.index = -1
	a.mu.Unlock()
	start := 0.0
	if position != nil {
		start = max(0, *position)
	}
	a.startTrackAt(index, play == nil || *play, start, true)
}

// keptShuffle is the random order of next: the songs prev held keep their order among themselves, the new
// ones follow in a random order of their own.
func keptShuffle(prev []model.Track, order []int, next []model.Track) []int {
	rank := map[string]int{}
	for r, normal := range order {
		if normal < len(prev) {
			rank[prev[normal].Key()] = r
		}
	}
	var kept, added []int
	for i, t := range next {
		if _, ok := rank[t.Key()]; ok {
			kept = append(kept, i)
		} else {
			added = append(added, i)
		}
	}
	slices.SortFunc(kept, func(x, y int) int { return rank[next[x].Key()] - rank[next[y].Key()] })
	for _, i := range newShuffle(len(added)) {
		kept = append(kept, added[i])
	}
	return kept
}
