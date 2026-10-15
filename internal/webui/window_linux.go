package webui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"simmusic/internal/core"
)

// The tray icon, in the format the system's tray takes: a PNG on Linux.
var TrayICO = IconPNG

// TextFont names the interface font the user chose, for the desktop lyrics. On Linux the CEF host draws
// them and looks the font up by its family; an empty name is the system's default.
func TextFont(a *core.App) []byte { return []byte(a.Cfg.String("general.font")) }

// What the host reports of the main window: there is no handle to ask the system with.
var hostState struct{ maximized, active atomic.Bool }

// hostFields splits a line of the host into its fields.
func hostFields(line string) []string {
	fields := strings.Split(line, "\t")
	for i, field := range fields {
		fields[i] = hostUnescaper.Replace(field)
	}
	return fields
}

// Run starts the CEF host (runtime/SimMusicCef beside the program) and serves it until the program quits.
// The host owns every window: the main one, the extension views, the dialogs, and on Linux also the
// menus, the notices and the desktop lyrics. Commands go to its standard input, events come from its output.
func (u *UI) Run() (runErr error) {
	url, err := u.Start()
	if err != nil {
		return err
	}
	defer u.server.Shutdown(context.Background())
	exe, _ := os.Executable()
	host := filepath.Join(filepath.Dir(exe), "runtime", "SimMusicCef")
	if _, err = os.Stat(host); err != nil {
		return fmt.Errorf("找不到 CEF 宿主 %s，请运行 build-linux.sh：%w", host, err)
	}
	// The host loads the icon font from a file: menus and the desktop lyrics draw their glyphs with it.
	icons := filepath.Join(u.app.DataDir, "remixicon.ttf")
	if current, _ := os.ReadFile(icons); !bytes.Equal(current, RemixTTF) {
		_ = os.WriteFile(icons, RemixTTF, 0o644)
	}
	cmd := exec.Command(host, "--simmusic-url="+url, "--simmusic-cache="+filepath.Join(u.app.DataDir, "cef-cache"), "--simmusic-log="+filepath.Join(u.app.DataDir, "logs", "cef.log"),
		"--simmusic-icon="+filepath.Join(u.app.DataDir, "app-icon.png"), "--simmusic-icon-font="+icons,
		// The host places and shapes its windows through X11; under Wayland it runs on XWayland.
		"--ozone-platform=x11", "--enable-smooth-scrolling", "--disable-spell-checking")
	// See the Windows host: extension pages are not held to cross-origin response headers.
	cmd.Args = append(cmd.Args, "--disable-web-security")
	if port, err := strconv.Atoi(os.Getenv("SIMMUSIC_CEF_DEBUG")); err == nil && port >= 1024 && port <= 65535 {
		cmd.Args = append(cmd.Args, "--remote-debugging-port="+strconv.Itoa(port))
	}
	cmd.Dir = filepath.Dir(host)
	// The host and Chromium's helper processes form one group, and none of them outlives this program.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	defer func() {
		if runErr == nil {
			for _, name := range []string{"cef-cache", "cef-cache-106"} {
				if err := clearRuntimeCache(u.app.DataDir, name); err != nil {
					log.Printf("清理 CEF 缓存: %v", err)
				}
			}
		}
	}()
	if err = cmd.Start(); err != nil {
		return err
	}
	u.attachHost(stdin)
	if u.desk != nil {
		u.desk.Attach(u.hostSend)
	}
	go u.syncSleepWindow()
	go u.syncMedia()
	go func() {
		scanner := bufio.NewScanner(stdout)
		// A page message (a picture, for one) travels as a single line.
		scanner.Buffer(make([]byte, 64<<10), 256<<20)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "WV:"):
				u.hostEvent(strings.TrimPrefix(line, "WV:"))
			case strings.HasPrefix(line, "READY:"):
				// The main window exists. The rest of the program asks for a window handle to know that.
				u.hwnd.Store(1)
			case strings.HasPrefix(line, "STATE:"):
				// maximized, active
				if fields := hostFields(strings.TrimPrefix(line, "STATE:")); len(fields) >= 2 {
					hostState.maximized.Store(fields[0] == "1")
					hostState.active.Store(fields[1] == "1")
				}
			case strings.HasPrefix(line, "OV:"):
				u.overlay.HostEvent(hostFields(strings.TrimPrefix(line, "OV:")))
			case strings.HasPrefix(line, "DL:"):
				if u.desk != nil {
					// The handlers save the position and act on the player: not on the reader's goroutine.
					go u.desk.HostEvent(hostFields(strings.TrimPrefix(line, "DL:")))
				}
			case strings.HasPrefix(line, "DRAG:"):
				// The files of the drag now over the window; a drop from the page refers to these.
				var paths []string
				if list := strings.TrimPrefix(line, "DRAG:"); list != "" {
					paths = strings.Split(list, "\t")
				}
				u.mu.Lock()
				u.dragPaths = paths
				u.mu.Unlock()
			default:
				log.Println("cef:", line)
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		return err
	case <-u.quit:
		u.window("destroy")
		select {
		case err = <-done:
			return err
		case <-time.After(5 * time.Second):
			log.Printf("CEF 宿主未在 5 秒内退出，尚未关闭的 webview：%v", u.openViews())
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return <-done
		}
	}
}

// window does to the main window what the host page or the program asks for.
func (u *UI) window(action string) {
	if u.hwnd.Load() == 0 {
		return
	}
	switch action {
	case "show":
		u.ShowWindow(true)
	case "pageLoading", "pageReady", "sleepPageReady", "minimize", "maximize", "destroy", "sleepShow", "sleepHide":
		u.hostSend("host", action)
	case "close":
		if u.app.Cfg.Bool("general.noBackground") {
			u.Quit()
		} else {
			u.ShowWindow(false)
		}
	case "quit":
		u.Quit()
	}
}

func (u *UI) ShowWindow(show bool) {
	if show {
		u.hostSend("host", "show")
	} else {
		u.hostSend("host", "hide")
	}
}

// launchFromHost is what lets the foreground process open folders and addresses on Windows. Nothing
// of the kind is needed here: this process starts them itself.
func (u *UI) launchFromHost(kind, target string) bool { return false }

func (u *UI) maximized() bool { return hostState.maximized.Load() }

// mainUncovered reports whether the main window is the one in front. Whether another window lies over a
// part of it cannot be told here as it can on Windows.
func (u *UI) mainUncovered() bool { return hostState.active.Load() }
