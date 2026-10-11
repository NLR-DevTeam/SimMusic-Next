package config

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentTimerAndManualFlushes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	s := Open(path, nil)
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			s.Set("revision", i)
			if err := s.Flush(); err != nil {
				t.Errorf("concurrent flush: %v", err)
			}
		})
	}
	wg.Wait()
	s.Set("revision", 999)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if Open(path, nil).Int("revision") != 999 {
		t.Fatal("older snapshot replaced the latest index")
	}
}
