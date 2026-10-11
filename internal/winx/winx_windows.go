// Package winx 封装 Windows 平台相关的系统调用。
package winx

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	powrprof                     = windows.NewLazySystemDLL("powrprof.dll")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrame           = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	procSetSuspendState          = powrprof.NewProc("SetSuspendState")
	procMessageBeep              = user32.NewProc("MessageBeep")
)

const (
	SW_HIDE    = 0
	SW_SHOW    = 5
	SW_RESTORE = 9
)

func IsVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

// ExcludeFromCapture 使窗口在截图 / 录屏中隐藏（Win10 2004+）。
func ExcludeFromCapture(hwnd uintptr, exclude bool) {
	if hwnd == 0 {
		return
	}
	var v uintptr
	if exclude {
		v = 0x11 // WDA_EXCLUDEFROMCAPTURE
	}
	procSetWindowDisplayAffinity.Call(hwnd, v)
}

// StyleWindow 为无边框窗口启用 Win11 圆角与 DWM 阴影。
func StyleWindow(hwnd uintptr) {
	if hwnd == 0 || procDwmSetWindowAttribute.Find() != nil {
		return
	}
	pref := int32(2) // DWMWCP_ROUND
	procDwmSetWindowAttribute.Call(hwnd, 33, uintptr(unsafe.Pointer(&pref)), 4)
	margins := [4]int32{0, 0, 0, 1}
	procDwmExtendFrame.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
}

// Suspend 让系统进入睡眠。
func Suspend() error {
	if err := procSetSuspendState.Find(); err != nil {
		return err
	}
	procSetSuspendState.Call(0, 0, 0)
	return nil
}

func Shutdown() error {
	cmd := exec.Command("shutdown", "/s", "/t", "0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func Beep() { procMessageBeep.Call(0x40) }

// RegisterFileMenu 在资源管理器右键菜单为指定扩展名添加 / 移除「使用 SimMusic 播放」。
func RegisterFileMenu(exts []string, enable bool, label string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var firstErr error
	// ProgID：出现在「打开方式」列表中；设为默认程序后文件使用 file.png 图标（资源 ID 2）
	const progID = `Software\Classes\SimMusicNext.Audio`
	if enable {
		if k, _, err := registry.CreateKey(registry.CURRENT_USER, progID, registry.ALL_ACCESS); err == nil {
			k.SetStringValue("", "SimMusic 音频文件")
			k.SetStringValue("FriendlyTypeName", "SimMusic 音频文件")
			k.Close()
		}
		if k, _, err := registry.CreateKey(registry.CURRENT_USER, progID+`\DefaultIcon`, registry.ALL_ACCESS); err == nil {
			k.SetStringValue("", exe+",-2")
			k.Close()
		}
		if k, _, err := registry.CreateKey(registry.CURRENT_USER, progID+`\shell\open\command`, registry.ALL_ACCESS); err == nil {
			k.SetStringValue("", `"`+exe+`" "%1"`)
			k.Close()
		}
	} else {
		for _, sub := range []string{`\shell\open\command`, `\shell\open`, `\shell`, `\DefaultIcon`, ``} {
			_ = registry.DeleteKey(registry.CURRENT_USER, progID+sub)
		}
	}
	for _, ext := range exts {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		base := `Software\Classes\SystemFileAssociations\` + ext + `\shell\SimMusicNext`
		owp := `Software\Classes\` + ext + `\OpenWithProgids`
		if k, _, err := registry.CreateKey(registry.CURRENT_USER, owp, registry.ALL_ACCESS); err == nil {
			if enable {
				k.SetBinaryValue("SimMusicNext.Audio", nil)
			} else {
				k.DeleteValue("SimMusicNext.Audio")
			}
			k.Close()
		}
		if !enable {
			_ = registry.DeleteKey(registry.CURRENT_USER, base+`\command`)
			_ = registry.DeleteKey(registry.CURRENT_USER, base)
			continue
		}
		k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.ALL_ACCESS)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		k.SetStringValue("", label)
		k.SetStringValue("Icon", exe)
		k.Close()
		c, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\command`, registry.ALL_ACCESS)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		c.SetStringValue("", `"`+exe+`" "%1"`)
		c.Close()
	}
	return firstErr
}

// Launch 由界面提供：让拥有前台窗口的进程去打开目录（"open"）、选中文件（"select"）或网址（"url"）。
// Windows 只允许前台进程把新窗口带到前面；本进程是后台进程，自己启动的资源管理器会开在主窗口后面。
// 未设置或返回 false 时由本进程直接启动。
var Launch func(kind, target string) bool

// OpenPath 用资源管理器打开路径；网址交给默认浏览器。
func OpenPath(p string) {
	// explorer 遇到带 # 或 ? 的网址会打开一个资源管理器窗口而不是浏览器。
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		if Launch == nil || !Launch("url", p) {
			_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", p).Start()
		}
		return
	}
	if Launch != nil && Launch("open", p) {
		return
	}
	cmd := exec.Command("explorer", p)
	_ = cmd.Start()
}

// RevealFile 在资源管理器中选中文件。
func RevealFile(p string) {
	if Launch != nil && Launch("select", p) {
		return
	}
	cmd := exec.Command("explorer")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer /select,"` + p + `"`}
	_ = cmd.Start()
}

var (
	procShowWindowAsync = user32.NewProc("ShowWindowAsync")
	procIsIconic        = user32.NewProc("IsIconic")
)

// ShowWindowAsync 异步显示 / 隐藏窗口，可在任意线程调用而不会因跨线程消息造成死锁。
func ShowWindowAsync(hwnd uintptr, show bool) {
	if hwnd == 0 {
		return
	}
	if show {
		procShowWindowAsync.Call(hwnd, SW_SHOW)
		if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
			procShowWindowAsync.Call(hwnd, SW_RESTORE)
		}
		procSetForegroundWindow.Call(hwnd)
	} else {
		procShowWindowAsync.Call(hwnd, SW_HIDE)
	}
}

// FocusContent 在主窗口位于前台、但键盘焦点停留在窗口框架上时，把焦点交给其内容子窗口。
// 须在已锁定的系统线程上调用：跨线程设置焦点需要临时附加输入队列。
func FocusContent(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if foreground, _, _ := user32.NewProc("GetForegroundWindow").Call(); foreground != hwnd {
		return
	}
	thread, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(hwnd, 0)
	var info struct {
		Size, Flags                                      uint32
		Active, Focus, Capture, MenuOwner, MoveSize, Caret uintptr
		CaretRect                                        [4]int32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := user32.NewProc("GetGUIThreadInfo").Call(thread, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return
	}
	// 拖动、缩放窗口或菜单打开期间不干预；焦点已在子窗口时无需处理。
	if info.Flags&0x1e != 0 || info.Focus != 0 && info.Focus != hwnd {
		return
	}
	child, _, _ := user32.NewProc("GetWindow").Call(hwnd, 5) // GW_CHILD
	if child == 0 || !IsVisible(child) {
		return
	}
	attach := user32.NewProc("AttachThreadInput")
	if ok, _, _ := attach.Call(uintptr(windows.GetCurrentThreadId()), thread, 1); ok == 0 {
		return
	}
	user32.NewProc("SetFocus").Call(child)
	attach.Call(uintptr(windows.GetCurrentThreadId()), thread, 0)
}

// IsIconic 报告窗口是否最小化。
func IsIconic(hwnd uintptr) bool {
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}

// IsForegroundUncovered checks the main client area against windows above it.
// The desktop-lyrics overlay is excluded so it cannot hide itself on alternate ticks.
func IsForegroundUncovered(hwnd, overlay uintptr) bool {
	if hwnd == 0 || !IsVisible(hwnd) || IsIconic(hwnd) {
		return false
	}
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	root, _, _ := user32.NewProc("GetAncestor").Call(foreground, 2) // GA_ROOT
	if root != hwnd {
		return false
	}
	type rect struct{ Left, Top, Right, Bottom int32 }
	type point struct{ X, Y int32 }
	var client rect
	var origin point
	user32.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&client)))
	user32.NewProc("ClientToScreen").Call(hwnd, uintptr(unsafe.Pointer(&origin)))
	client.Left, client.Right = client.Left+origin.X, client.Right+origin.X
	client.Top, client.Bottom = client.Top+origin.Y, client.Bottom+origin.Y
	getWindow := user32.NewProc("GetWindow")
	getRect := user32.NewProc("GetWindowRect")
	getAttribute := dwmapi.NewProc("DwmGetWindowAttribute")
	getProcess := user32.NewProc("GetWindowThreadProcessId")
	var own uint32
	getProcess.Call(hwnd, uintptr(unsafe.Pointer(&own)))
	for h, _, _ := getWindow.Call(hwnd, 3); h != 0; h, _, _ = getWindow.Call(h, 3) { // GW_HWNDPREV
		if h == overlay || !IsVisible(h) || IsIconic(h) {
			continue
		}
		// The window's own tooltips and popups are part of it, not something covering it. A tooltip
		// coming and going over a control would otherwise show and hide the desktop lyrics each time.
		var process uint32
		getProcess.Call(h, uintptr(unsafe.Pointer(&process)))
		if process == own {
			continue
		}
		var cloaked uint32
		getAttribute.Call(h, 14, uintptr(unsafe.Pointer(&cloaked)), 4) // DWMWA_CLOAKED
		if cloaked != 0 {
			continue
		}
		var r rect
		getRect.Call(h, uintptr(unsafe.Pointer(&r)))
		if r.Left < client.Right && r.Right > client.Left && r.Top < client.Bottom && r.Bottom > client.Top {
			return false
		}
	}
	return true
}

// RegisterAppIdentity 为 AppUserModelID 登记显示名称与图标。未打包的桌面程序没有应用清单，
// 系统媒体控件等界面靠这一项得知该 ID 是谁，否则显示为「Unknown app」。
func RegisterAppIdentity(id, name, iconPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+id, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("DisplayName", name); err != nil {
		return err
	}
	return k.SetStringValue("IconUri", iconPath)
}
