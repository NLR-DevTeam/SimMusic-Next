package webui

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"simmusic/internal/core"
	"simmusic/internal/winx"
)

// The tray icon, in the format the system's tray takes.
var TrayICO, _ = files.ReadFile("web/assets/tray.ico")

// TextFont is the file of the interface font the user chose (Microsoft YaHei when none), for the
// desktop lyrics, which draw their own text. No font ships with the application.
func TextFont(a *core.App) []byte {
	data, err := winx.FontData(a.Cfg.String("general.font"))
	if err != nil {
		data, _ = winx.FontData(winx.DefaultFont)
	}
	return data
}

func (u *UI) Run() (runErr error) {
	url, err := u.Start()
	if err != nil {
		return err
	}
	defer u.server.Shutdown(context.Background())
	exe, _ := os.Executable()
	host := filepath.Join(filepath.Dir(exe), "runtime", "SimMusicCef.exe")
	if _, err = os.Stat(host); err != nil {
		return fmt.Errorf("找不到 CEF 宿主 %s，请运行 build.ps1：%w", host, err)
	}
	cmd := exec.Command(host, "--simmusic-url="+url, "--simmusic-cache="+filepath.Join(u.app.DataDir, "cef-cache"), "--simmusic-log="+filepath.Join(u.app.DataDir, "logs", "cef.log"),
		"--enable-smooth-scrolling", "--enable-features=WindowsScrollingPersonality,FluentScrollbar")
	// Pages of extension webviews are not held to cross-origin response headers (Access-Control-Allow-*).
	// Chromium 106 only offers this for the whole browser process; the interface page stays confined by its
	// own Content-Security-Policy, and its API by the access token and the Origin check.
	cmd.Args = append(cmd.Args, "--disable-web-security")
	// Explicit development opt-in; normal builds expose no debugging endpoint.
	if port, err := strconv.Atoi(os.Getenv("SIMMUSIC_CEF_DEBUG")); err == nil && port >= 1024 && port <= 65535 {
		cmd.Args = append(cmd.Args, "--remote-debugging-port="+strconv.Itoa(port))
	}
	cmd.Dir = filepath.Dir(host)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer func() {
		windows.CloseHandle(job)
		if runErr == nil {
			for _, name := range []string{"cef-cache", "cef-cache-106"} {
				if err := clearRuntimeCache(u.app.DataDir, name); err != nil {
					log.Printf("清理 CEF 缓存: %v", err)
				}
			}
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("CEF process ownership: %w", err)
	}
	u.attachHost(stdin)
	go u.keepFocus()
	go u.syncSleepWindow()
	go u.syncMedia()
	go func() {
		scanner := bufio.NewScanner(stdout)
		// A page message (a picture, for one) travels as a single line.
		scanner.Buffer(make([]byte, 64<<10), 256<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "WV:") {
				u.hostEvent(strings.TrimPrefix(line, "WV:"))
			} else if strings.HasPrefix(line, "HWND:") {
				h, _ := strconv.ParseUint(strings.TrimPrefix(line, "HWND:"), 10, 64)
				u.hwnd.Store(h)
			} else if strings.HasPrefix(line, "MEDIA:") {
				go u.mediaAction(strings.TrimPrefix(line, "MEDIA:"))
			} else if strings.HasPrefix(line, "DRAG:") {
				// The files of the drag now over the window; a drop from the page refers to these.
				var paths []string
				if list := strings.TrimPrefix(line, "DRAG:"); list != "" {
					paths = strings.Split(list, "\t")
				}
				u.mu.Lock()
				u.dragPaths = paths
				u.mu.Unlock()
			} else {
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
			cmd.Process.Kill()
			return <-done
		}
	}
}

func (u *UI) window(action string) {
	h := uintptr(u.hwnd.Load())
	if h == 0 {
		return
	}
	dll := windows.NewLazySystemDLL("user32.dll")
	switch action {
	case "show":
		u.ShowWindow(true)
	case "pageLoading":
		dll.NewProc("PostMessageW").Call(h, 0x8006, 0, 0)
	case "pageReady":
		dll.NewProc("PostMessageW").Call(h, 0x8004, 0, 0)
	case "sleepPageReady":
		dll.NewProc("PostMessageW").Call(h, 0x8005, 0, 0)
	case "minimize":
		dll.NewProc("PostMessageW").Call(h, 0x0112, 0xf020, 0)
	case "maximize":
		v, _, _ := dll.NewProc("IsZoomed").Call(h)
		var mode uintptr = 0xf030
		if v != 0 {
			mode = 0xf120
		}
		dll.NewProc("PostMessageW").Call(h, 0x0112, mode, 0)
	case "close":
		if u.app.Cfg.Bool("general.noBackground") {
			u.Quit()
		} else {
			u.ShowWindow(false)
		}
	case "sleepShow":
		dll.NewProc("PostMessageW").Call(h, 0x8002, 0, 0)
	case "sleepHide":
		dll.NewProc("PostMessageW").Call(h, 0x8003, 0, 0)
	case "destroy":
		dll.NewProc("PostMessageW").Call(h, 0x8001, 0, 0)
	case "quit":
		u.Quit()
	}
}

// keepFocus hands keyboard focus back to the page whenever the window returns to the
// foreground; Windows otherwise leaves it on the frame and page shortcuts stop working.
func (u *UI) keepFocus() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-u.quit:
			return
		case <-tick.C:
			winx.FocusContent(uintptr(u.hwnd.Load()))
		}
	}
}

func (u *UI) ShowWindow(show bool) { winx.ShowWindowAsync(uintptr(u.hwnd.Load()), show) }

// mainUncovered reports whether the main window is the one in front with nothing but the desktop lyrics over it.
func (u *UI) mainUncovered() bool {
	return winx.IsForegroundUncovered(uintptr(u.hwnd.Load()), u.desk.Handle())
}

func (u *UI) maximized() bool {
	value, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("IsZoomed").Call(uintptr(u.hwnd.Load()))
	return value != 0
}
