// Package wake keeps the computer's sleep out of the waits of this program. The clock behind Go's timers runs
// on through sleep on Windows, so a timer set before it ends the moment the computer wakes, however little of
// the wait had gone by; and connections do not survive sleep, the network taking some seconds more to return.
package wake

import (
	"sync"
	"time"
)

const (
	beatEvery = time.Second
	// More than this between two beats is no busy program: the clock jumped.
	jump = 8 * time.Second
	// How long the network is given to come back.
	settle = 10 * time.Second
)

var (
	mu     sync.Mutex
	last   = now()
	asleep time.Duration
	woke   time.Time
	hooks  []func()
)

// now reads the wall clock: the monotonic one stops during sleep on Linux and so shows no jump there.
func now() time.Time { return time.Now().Round(0) }

func init() {
	go func() {
		for range time.Tick(beatEvery) {
			beat()
		}
	}()
}

// beat notices a jump of the clock and returns the time slept through so far. Timers fire the moment the
// computer wakes, in no fixed order, so whoever looks at the time passing calls it first.
func beat() time.Duration {
	mu.Lock()
	at := now()
	passed := at.Sub(last)
	last = at
	var run []func()
	if passed > jump || passed < 0 {
		asleep += passed
		woke = at
		run = hooks
	}
	total := asleep
	mu.Unlock()
	for _, hook := range run {
		hook()
	}
	return total
}

// OnResume adds what is to run whenever the computer has woken.
func OnResume(hook func()) {
	mu.Lock()
	hooks = append(hooks[:len(hooks):len(hooks)], hook)
	mu.Unlock()
}

// After is time.After for a wait that only the time awake counts towards. The second result ends it early.
func After(d time.Duration) (<-chan struct{}, func()) {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		start, slept := now(), beat()
		for left := d; left > 0; left = d - (now().Sub(start) - (beat() - slept)) {
			timer := time.NewTimer(left)
			select {
			case <-timer.C:
			case <-stopped:
				timer.Stop()
				return
			}
		}
		close(done)
	}()
	return done, sync.OnceFunc(func() { close(stopped) })
}

// Interrupted reports whether a request begun at since met the computer's sleep: it slept meanwhile, or had
// just woken and the network may not have been back.
func Interrupted(since time.Time) bool {
	beat()
	mu.Lock()
	defer mu.Unlock()
	return !woke.IsZero() && woke.Add(settle).After(since)
}

// Settle returns once the network has had its time to come back after the last wake.
func Settle() {
	beat()
	mu.Lock()
	wait := time.Until(woke.Add(settle))
	mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}
