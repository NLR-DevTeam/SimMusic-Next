package webui

import (
	"log"
	"runtime"
	"time"
)

// Fast core operations should never wait indefinitely. Native pickers and
// plugin scans are deliberately excluded because they can take user time.
func (u *UI) watchRequest(action string) func() {
	switch action {
	case "snapshot", "toggle", "next", "previous", "play", "playIndex", "seek", "volume", "mode", "appendQueue", "playNext", "removeQueue", "clearQueue":
	default:
		return func() {}
	}
	timer := time.AfterFunc(8*time.Second, func() {
		now := time.Now().Unix()
		previous := u.lastHangDump.Load()
		if now-previous < 60 || !u.lastHangDump.CompareAndSwap(previous, now) {
			return
		}
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		log.Printf("core request %q has not returned after 8 seconds; goroutine dump:\n%s", action, stack[:n])
	})
	return func() { timer.Stop() }
}
