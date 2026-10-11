package core

import (
	"math/rand/v2"
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
