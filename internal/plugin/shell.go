package plugin

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dop251/goja"

	"simmusic/internal/winx"
)

// Programs an extension runs (the "shell" permission): sim.shell.spawn starts one and stays connected
// to it, sim.shell.exec runs one to its end. Whatever is still running when the extension stops, is
// reloaded or removed is ended with it, and so is everything when the host itself goes.

const (
	maxChildren    = 16
	maxExecOutput  = 16 << 20
	defaultExecMax = 60 * time.Second
)

var childEvents = []string{"stdout", "stderr", "exit"}

// children are the programs of one run of an extension.
type children struct {
	mu     sync.Mutex
	list   map[*exec.Cmd]chan struct{} // closed once the program has been waited for
	group  *winx.ChildGroup            // the programs and everything they started in turn
	closed bool
}

func (c *children) add(cmd *exec.Cmd) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("扩展已停止")
	}
	if len(c.list) >= maxChildren {
		return fmt.Errorf("同时运行的程序不能超过 %d 个", maxChildren)
	}
	c.list[cmd] = make(chan struct{})
	return nil
}

// remove is called by whoever waited for the program, when it is gone.
func (c *children) remove(cmd *exec.Cmd) {
	c.mu.Lock()
	if done, ok := c.list[cmd]; ok {
		close(done)
		delete(c.list, cmd)
	}
	c.mu.Unlock()
}

// killAll ends every program of the extension, with whatever those started, and returns once they are
// gone: the files of the extension (a program it ships with among them) can then be replaced or removed.
func (c *children) killAll() {
	c.mu.Lock()
	c.closed = true
	var waiting []chan struct{}
	for cmd, done := range c.list {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		waiting = append(waiting, done)
	}
	c.mu.Unlock()
	c.group.Kill()
	limit := time.After(5 * time.Second)
	for _, done := range waiting {
		select {
		case <-done:
		case <-limit:
			return
		}
	}
}

// command builds the program from what the script passed: (path, args?, { cwd, env, hide }?).
func (p *Plugin) command(call goja.FunctionCall) (*exec.Cmd, map[string]any) {
	p.need(PermShell)
	path := call.Argument(0).String()
	if goja.IsUndefined(call.Argument(0)) || !filepath.IsAbs(path) {
		p.throw(errors.New("程序路径必须是绝对路径；扩展自带的程序用 sim.path.join(sim.plugin.dir, ...) 构造"))
	}
	var args []string
	if list, ok := exportJSON(call.Argument(1)).([]any); ok {
		for _, item := range list {
			args = append(args, fmt.Sprint(item))
		}
	}
	options, _ := exportJSON(call.Argument(2)).(map[string]any)
	cmd := exec.Command(path, args...)
	cmd.Dir = filepath.Dir(path)
	if cwd, _ := options["cwd"].(string); cwd != "" {
		cmd.Dir = cwd
	}
	if env, _ := options["env"].(map[string]any); len(env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+fmt.Sprint(value))
		}
	}
	winx.PrepareChild(cmd, options["hide"] == true)
	return cmd, options
}

func (p *Plugin) installShell(rs *runState, shell *goja.Object) {
	rt := p.rt
	// stop() ends them itself, and waits, before it returns: disabling, reloading, replacing or removing
	// the extension and leaving the application all go through it.
	running := &children{list: map[*exec.Cmd]chan struct{}{}, group: winx.NewChildGroup()}
	rs.children = running
	start := func(cmd *exec.Cmd) {
		if err := running.add(cmd); err != nil {
			p.throw(err)
		}
		if err := cmd.Start(); err != nil {
			running.remove(cmd)
			p.throw(err)
		}
		running.group.Add(cmd.Process.Pid)
	}

	// spawn(path, args, options) returns at once with a handle: text written to it is the program's
	// standard input, each line the program prints arrives as a "stdout" or "stderr" event, its end as "exit".
	shell.Set("spawn", func(call goja.FunctionCall) goja.Value {
		cmd, _ := p.command(call)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			p.throw(err)
		}
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()
		start(cmd)
		obj := rt.NewObject()
		listeners := map[string][]goja.Value{}
		exited, code := false, -1
		emit := func(kind string, fields map[string]any) {
			event := rt.NewObject()
			event.Set("type", kind)
			event.Set("target", obj)
			for key, value := range fields {
				event.Set(key, value)
			}
			for _, handler := range append([]goja.Value{obj.Get("on" + kind)}, listeners[kind]...) {
				if fn, ok := goja.AssertFunction(handler); ok {
					if _, err := fn(obj, event); err != nil {
						p.logf("子进程 %s 事件处理出错: %v", kind, err)
					}
				}
			}
			_, _ = rt.RunString("void 0")
		}
		gone := make(chan struct{})
		var readers sync.WaitGroup
		read := func(kind string, source io.Reader) {
			defer readers.Done()
			scanner := bufio.NewScanner(source)
			scanner.Buffer(make([]byte, 64<<10), 8<<20)
			for scanner.Scan() {
				line := scanner.Text()
				if !p.postState(rs, func() { emit(kind, map[string]any{"data": line}) }) {
					break
				}
			}
			// A line beyond the limit, or a stopped extension: the rest is dropped so the program is not held up.
			io.Copy(io.Discard, source)
		}
		readers.Add(2)
		go read("stdout", stdout)
		go read("stderr", stderr)
		go func() {
			readers.Wait()
			err := cmd.Wait()
			close(gone)
			running.remove(cmd)
			status := cmd.ProcessState.ExitCode()
			if err != nil && status == 0 {
				status = -1
			}
			p.postState(rs, func() {
				exited, code = true, status
				emit("exit", map[string]any{"code": status})
			})
		}()
		obj.Set("pid", cmd.Process.Pid)
		for _, name := range childEvents {
			obj.Set("on"+name, goja.Null())
		}
		obj.DefineAccessorProperty("exited", rt.ToValue(func() bool { return exited }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
		obj.DefineAccessorProperty("exitCode", rt.ToValue(func() goja.Value {
			if !exited {
				return goja.Null()
			}
			return rt.ToValue(code)
		}), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
		known := func(name string) string {
			for _, event := range childEvents {
				if event == name {
					return name
				}
			}
			p.throw(errors.New("未知的子进程事件：" + name))
			return ""
		}
		obj.Set("on", func(name string, handler goja.Value) *goja.Object {
			if _, ok := goja.AssertFunction(handler); ok {
				listeners[known(name)] = append(listeners[name], handler)
			}
			return obj
		})
		obj.Set("off", func(name string, handler goja.Value) *goja.Object {
			list := listeners[known(name)]
			for i, item := range list {
				if item.SameAs(handler) {
					listeners[name] = append(append([]goja.Value(nil), list[:i]...), list[i+1:]...)
					break
				}
			}
			return obj
		})
		// write(text) reports whether the program took it; false once it has ended or closed its input.
		// Writes go out in order on a goroutine of their own: a program that does not read cannot stall the extension.
		writes := make(chan string, 1024)
		go func() {
			defer stdin.Close()
			for {
				select {
				case text, open := <-writes:
					if !open {
						return
					}
					if _, err := io.WriteString(stdin, text); err != nil {
						return
					}
				case <-gone:
					return
				}
			}
		}()
		ended := false
		obj.Set("write", func(text string) bool {
			if ended || exited {
				return false
			}
			select {
			case writes <- text:
				return true
			default:
				return false
			}
		})
		obj.Set("end", func() {
			if !ended {
				ended = true
				close(writes)
			}
		})
		obj.Set("kill", func() { cmd.Process.Kill() })
		return obj
	})

	// exec(path, args, { cwd, env, hide, input, timeout }) runs the program to its end:
	// Promise<{ code, stdout, stderr }>. timeout is in milliseconds, one minute when omitted.
	shell.Set("exec", func(call goja.FunctionCall) goja.Value {
		cmd, options := p.command(call)
		limit := defaultExecMax
		if ms, ok := optionInt(options, "timeout"); ok && ms > 0 {
			limit = time.Duration(ms) * time.Millisecond
		}
		if input, _ := options["input"].(string); input != "" {
			cmd.Stdin = bytes.NewReader([]byte(input))
		}
		var stdout, stderr capped
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		start(cmd)
		return p.async(func() (any, error) {
			timer := time.AfterFunc(limit, func() { cmd.Process.Kill() })
			err := cmd.Wait()
			running.remove(cmd)
			if !timer.Stop() {
				return nil, errors.New("程序运行超时")
			}
			code := cmd.ProcessState.ExitCode()
			if err != nil && code == 0 {
				return nil, err
			}
			return map[string]any{"code": code, "stdout": stdout.String(), "stderr": stderr.String()}, nil
		})
	})
}

// capped keeps the beginning of what a program printed and lets the rest go.
type capped struct{ bytes.Buffer }

func (c *capped) Write(b []byte) (int, error) {
	if room := maxExecOutput - c.Len(); room > 0 {
		c.Buffer.Write(b[:min(room, len(b))])
	}
	return len(b), nil
}
