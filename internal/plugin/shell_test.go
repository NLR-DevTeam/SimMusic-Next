package plugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"simmusic/internal/config"
)

// The test binary doubles as the program the extension runs: it answers each line and leaves with 3.
func TestMain(m *testing.M) {
	if os.Getenv("SIM_SHELL_CHILD") == "1" {
		fmt.Println("ready", strings.Join(os.Args[1:], ","))
		lines := bufio.NewScanner(os.Stdin)
		for lines.Scan() {
			if lines.Text() == "wait" {
				time.Sleep(time.Minute)
			}
			fmt.Println("got " + lines.Text())
		}
		fmt.Fprintln(os.Stderr, "bye")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

const shellScript = `
const exe = sim.settings.get("exe");
const env = { SIM_SHELL_CHILD: "1" };
exports.spawn = () => new Promise(resolve => {
    const seen = [], errors = [];
    const child = sim.shell.spawn(exe, ["a", 2], { env });
    child.onstdout = event => { seen.push(event.data); if (event.data.startsWith("ready")) { child.write("one\n"); child.write("two\n"); child.end(); } };
    child.on("stderr", event => errors.push(event.data));
    child.on("exit", event => resolve({ seen, errors, code: event.code, exited: child.exited, exitCode: child.exitCode, late: child.write("x") }));
});
exports.exec = () => sim.shell.exec(exe, [], { env, input: "in\n" });
exports.timeout = () => sim.shell.exec(exe, [], { env, input: "wait\n", timeout: 200 });
exports.hang = () => { sim.shell.spawn(sim.settings.get("copy"), [], { env }).write("wait\n"); };
exports.relative = () => sim.shell.spawn("child.exe");
exports.dir = () => sim.plugin.dir;
`

func shellFixture(t *testing.T, permissions []string) *Plugin {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "data"), filepath.Join(dir, "extensions"), config.Open(filepath.Join(dir, "state.json"), nil), nil)
	manifest, _ := json.Marshal(Manifest{ID: "test", Permissions: permissions})
	if err := m.add(fstest.MapFS{"plugin.json": {Data: manifest}, "main.js": {Data: []byte(shellScript)}}, "", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.StopAll)
	exe, _ := os.Executable()
	m.Get("test").Settings.Set("exe", exe)
	m.Reload("test")
	return m.Get("test")
}

func TestShellSpawn(t *testing.T) {
	p := shellFixture(t, []string{PermShell})
	result, err := p.Call(10*time.Second, "spawn")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(result)
	if string(got) != `{"code":3,"errors":["bye"],"exitCode":3,"exited":true,"late":false,"seen":["ready a,2","got one","got two"]}` {
		t.Fatalf("spawn: %s", got)
	}
	result, err = p.Call(10*time.Second, "exec")
	if out, _ := result.(map[string]any); err != nil || out["code"] != int64(3) || strings.ReplaceAll(fmt.Sprint(out["stdout"]), "\r", "") != "ready \ngot in\n" || !strings.HasPrefix(fmt.Sprint(out["stderr"]), "bye") {
		t.Fatalf("exec: %v %v", result, err)
	}
	if _, err = p.Call(10*time.Second, "timeout"); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("a program past its time limit: %v", err)
	}
	if _, err = p.Call(time.Second, "relative"); err == nil {
		t.Fatal("a relative program path was accepted")
	}
}

func TestShellRequiresPermission(t *testing.T) {
	p := shellFixture(t, nil)
	for _, name := range []string{"spawn", "exec"} {
		if _, err := p.Call(time.Second, name); err == nil || !strings.Contains(err.Error(), "shell") {
			t.Fatalf("%s without the permission: %v", name, err)
		}
	}
}

// A program still running when the extension stops has been ended by the time stop returns: its own file,
// which the system keeps locked while it runs, can be deleted at once, as replacing an extension does.
func TestShellChildEndsWithExtension(t *testing.T) {
	p := shellFixture(t, []string{PermShell})
	exe, _ := os.Executable()
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "extension")
	copied := filepath.Join(dir, "helper.exe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, data, 0o755); err != nil {
		t.Fatal(err)
	}
	p.Settings.Set("copy", copied)
	if _, err := p.Call(5*time.Second, "hang"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	p.stop()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("the program's file is still held after the extension stopped: %v", err)
	}
	// The program would sleep for a minute; stopping must not wait for that.
	if time.Since(started) > 5*time.Second {
		t.Fatal("stopping the extension waited for its program")
	}
}
