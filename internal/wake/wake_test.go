package wake

import (
	"testing"
	"time"
)

func TestAfterEndsAndStops(t *testing.T) {
	begun := time.Now()
	done, stop := After(30 * time.Millisecond)
	defer stop()
	select {
	case <-done:
		if time.Since(begun) < 30*time.Millisecond {
			t.Fatal("ended early")
		}
	case <-time.After(time.Second):
		t.Fatal("never ended")
	}
	done, stop = After(time.Hour)
	stop()
	stop()
	select {
	case <-done:
		t.Fatal("a stopped wait must not end")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestClockJumpCountsAsSleep(t *testing.T) {
	if Interrupted(time.Now()) {
		t.Fatal("no sleep yet")
	}
	resumed := make(chan struct{}, 1)
	OnResume(func() { resumed <- struct{}{} })
	before := time.Now()
	mu.Lock()
	last = last.Add(-time.Hour)
	mu.Unlock()
	if slept := beat(); slept < time.Hour {
		t.Fatalf("slept %v", slept)
	}
	select {
	case <-resumed:
	default:
		t.Fatal("hook did not run")
	}
	if !Interrupted(before) || !Interrupted(time.Now()) {
		t.Fatal("a request around the wake must count as interrupted")
	}
	if Interrupted(time.Now().Add(settle + time.Second)) {
		t.Fatal("a request after the network is back is not interrupted")
	}
}
