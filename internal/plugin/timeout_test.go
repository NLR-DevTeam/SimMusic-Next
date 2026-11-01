package plugin

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"simmusic/internal/config"
)

func TestCallTimeoutLeavesExtensionRunning(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, filepath.Join(dir, "plugins"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	p := addSchemeFixture(t, m, Manifest{ID: "slow", Version: "1"}, `exports.pending = () => new Promise(() => {}); exports.spin = () => { for (;;) {} }; exports.ping = () => "pong";`)
	defer m.StopAll()
	ping := func() {
		t.Helper()
		var answer string
		if err := p.CallInto(5*time.Second, &answer, "ping"); err != nil || answer != "pong" {
			t.Fatalf("extension no longer answers: %q %v", answer, err)
		}
	}
	if _, err := p.Call(20*time.Millisecond, "pending"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected a timeout: %v", err)
	}
	if !p.Running() || p.LoadErr() != nil {
		t.Fatalf("a slow call must not unload the extension: running %v, %v", p.Running(), p.LoadErr())
	}
	ping()
	defer func(previous time.Duration) { stuckAfter = previous }(stuckAfter)
	stuckAfter = 0
	if _, err := p.Call(20*time.Millisecond, "spin"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected a timeout: %v", err)
	}
	ping()
	if !p.Running() || p.LoadErr() != nil {
		t.Fatalf("a stuck job must cost that job alone: running %v, %v", p.Running(), p.LoadErr())
	}
}
