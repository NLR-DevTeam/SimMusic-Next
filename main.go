// SimMusic Next：Go 音频内核 + CEF / SimUI 界面，扩展由原 Goja 加载器运行。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"fyne.io/systray"

	"simmusic/internal/core"
	"simmusic/internal/desklyric"
	ui "simmusic/internal/webui"
	"simmusic/internal/winx"
	"simmusic/plugins/builtin"
)

// dataDir 返回程序目录下的 data 目录（便携存储：配置、扩展、日志全部位于此处）。
func dataDir() string {
	// Development and integration tests can isolate data without changing portable defaults.
	if override := os.Getenv("SIMMUSIC_DATA_DIR"); override != "" {
		if dir, err := filepath.Abs(override); err == nil {
			_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
			return dir
		}
	}
	base := "."
	if exe, err := os.Executable(); err == nil {
		base = filepath.Dir(exe)
		// go run 时可执行文件位于临时目录，改用当前工作目录
		if strings.Contains(strings.ToLower(base), strings.ToLower(os.TempDir())) {
			base, _ = os.Getwd()
		}
	}
	d := filepath.Join(base, "data")
	_ = os.MkdirAll(filepath.Join(d, "logs"), 0o755)
	return d
}

// forwardToRunning 尝试把参数转交给已运行的实例；成功时返回 true（当前进程应退出）。
func forwardToRunning(dir string, args []string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "instance.port"))
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strings.TrimSpace(string(b)), 400*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	msg, _ := json.Marshal(args)
	conn.Write(append(msg, '\n'))
	reply, _ := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimSpace(reply) == "ok"
}

func listenInstance(dir string, onArgs func([]string)) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	os.WriteFile(filepath.Join(dir, "instance.port"), []byte(strconv.Itoa(port)), 0o644)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				var args []string
				json.Unmarshal([]byte(line), &args)
				c.Write([]byte("ok\n"))
				onArgs(args)
			}()
		}
	}()
}

// setupLogs 将日志写入 data/logs，并把致命错误（包括任意 goroutine 的 panic）输出到 crash.log。
func setupLogs(dir string) {
	logDir := filepath.Join(dir, "logs")
	if f, err := os.OpenFile(filepath.Join(logDir, "simmusic.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		log.SetOutput(f)
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}
	if f, err := os.OpenFile(filepath.Join(logDir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		fmt.Fprintf(f, "\n===== 启动于 %s (%s) =====\n", time.Now().Format(time.DateTime), core.Version)
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	}
}

func main() {
	dir := dataDir()
	args := os.Args[1:]
	if forwardToRunning(dir, args) {
		return
	}
	setupLogs(dir)
	log.Println("SimMusic Next", core.Version, "data:", dir)
	// The system media controls name an unpackaged program by what is registered for its AppUserModelID.
	icon := filepath.Join(dir, "app-icon.png")
	if current, _ := os.ReadFile(icon); !bytes.Equal(current, ui.IconPNG) {
		_ = os.WriteFile(icon, ui.IconPNG, 0o644)
	}
	if err := winx.RegisterAppIdentity("SimMusic.Next", "SimMusic", icon); err != nil {
		log.Println("app identity:", err)
	}

	a, err := core.New(dir)
	if err != nil {
		log.Fatal(err)
	}
	builtinDir, deployErr := builtin.DeployLocal(dir)
	if deployErr != nil {
		log.Println("deploy builtin plugin:", deployErr)
	}
	a.Plugins.BeginLoading()
	if err := a.Plugins.AddBuiltinDir(builtinDir); err != nil {
		log.Println("builtin plugin:", err)
	}
	a.Plugins.LoadUserPlugins()
	a.RestoreQueue()

	// 桌面歌词
	desk := desklyric.New(ui.DesktopOptions(a), ui.TextFont(a), ui.RemixTTF)
	if y := a.State.Int("desk.y"); y > 0 {
		desk.SetPosition(a.State.Int("desk.x"), y)
	}
	desk.OnMoved = func(x, y int) { a.State.Set("desk.x", x); a.State.Set("desk.y", y) }
	a.Cfg.OnChange(func(key string, _ any) {
		if key == "general.font" {
			desk.SetTextFont(ui.TextFont(a))
		}
		if strings.HasPrefix(key, "dl.") || key == "lrc.transSize" || key == "lrc.multiLang" {
			desk.SetOptions(ui.DesktopOptions(a))
		}
	})

	u := ui.New(a, desk)
	quit := func() {
		a.SavePosition()
		_ = a.FlushMusicIndex()
		a.Plugins.StopAll()
		desk.Show(false)
		systray.Quit()
		os.Exit(0)
	}
	a.OnQuit = func() { u.Quit() }

	listenInstance(dir, func(args []string) {
		u.ShowWindow(true)
		go a.OpenFiles(args)
	})

	// 系统托盘（在独立的、锁定的系统线程上运行自己的消息循环）
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(ui.TrayICO)
			systray.SetTitle("SimMusic")
			systray.SetTooltip("SimMusic")
			systray.SetOnTapped(func() { u.ShowWindow(true) })
			mShow := systray.AddMenuItem("显示主界面", "")
			mPlay := systray.AddMenuItem("播放 / 暂停", "")
			mPrev := systray.AddMenuItem("上一首", "")
			mNext := systray.AddMenuItem("下一首", "")
			systray.AddSeparator()
			mLyric := systray.AddMenuItem("桌面歌词", "")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("退出 SimMusic", "")
			for {
				select {
				case <-mShow.ClickedCh:
					u.ShowWindow(true)
				case <-mPlay.ClickedCh:
					a.TogglePlay()
				case <-mPrev.ClickedCh:
					a.Prev()
				case <-mNext.ClickedCh:
					a.Next(false)
				case <-mLyric.ClickedCh:
					a.State.Set("desk.visible", !a.State.Bool("desk.visible"))
					u.Invalidate()
				case <-mQuit.ClickedCh:
					u.Quit()
				}
			}
		}, nil)
	}()

	go func() {
		a.ReloadMenus()
		if len(args) > 0 {
			a.OpenFiles(args)
		}
	}()

	if err := u.Run(); err != nil {
		log.Println("window:", err)
	}
	quit()
}
