// Package winx 在 Linux 上的对应实现：没有直接的系统 API 可调，改用桌面环境通用的命令行工具
// （xdg-open、zenity / kdialog、fontconfig、xclip / wl-copy、systemctl）。窗口相关的操作由 CEF 宿主完成，
// 这里按窗口句柄工作的函数因此是空操作。
package winx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultFont 是未选择界面字体时使用的字体族；由 fontconfig 解析成系统里实际存在的字体。
const DefaultFont = "sans-serif"

// Launch 由界面提供，含义同 Windows 版；Linux 上没有前台进程的限制，通常保持未设置。
var Launch func(kind, target string) bool

func start(name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, args...)
	if err = cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// OpenPath 用文件管理器打开路径；网址交给默认浏览器。
func OpenPath(p string) {
	if Launch != nil && Launch("open", p) {
		return
	}
	_ = start("xdg-open", p)
}

// RevealFile 在文件管理器中选中文件；文件管理器不支持时打开它所在的目录。
func RevealFile(p string) {
	if Launch != nil && Launch("select", p) {
		return
	}
	uri := "file://" + (&urlPath{p}).escaped()
	if start("dbus-send", "--session", "--dest=org.freedesktop.FileManager1", "--type=method_call", "/org/freedesktop/FileManager1",
		"org.freedesktop.FileManager1.ShowItems", "array:string:"+uri, "string:") != nil {
		_ = start("xdg-open", filepath.Dir(p))
	}
}

type urlPath struct{ path string }

// escaped percent-encodes what a file URI may not contain, keeping the slashes.
func (u *urlPath) escaped() string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(u.path); i++ {
		c := u.path[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/-_.~", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// CopyText 把文本放进剪贴板：Wayland 下用 wl-copy，X11 下用 xclip 或 xsel。
func CopyText(text string) error {
	tools := [][]string{{"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		tools = append([][]string{{"wl-copy"}}, tools...)
	}
	for _, tool := range tools {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err = cmd.Start(); err != nil {
			continue
		}
		// xclip stays alive to own the selection: it is not waited for here.
		go cmd.Wait()
		return nil
	}
	return errors.New("没有可用的剪贴板工具，请安装 wl-clipboard、xclip 或 xsel")
}

// PickPaths 打开系统的文件 / 目录选择对话框（zenity，没有则 kdialog）。owner 在 Linux 上不使用。
func PickPaths(owner uintptr, title string, folders bool, extensions []string) ([]string, error) {
	var patterns []string
	for _, ext := range extensions {
		patterns = append(patterns, "*."+strings.TrimPrefix(ext, "."))
	}
	var cmd *exec.Cmd
	separator := "\n"
	if path, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--title=" + title, "--separator=\n"}
		if folders {
			args = append(args, "--directory")
		} else {
			args = append(args, "--multiple")
			if len(patterns) > 0 {
				args = append(args, "--file-filter="+strings.Join(patterns, " "))
			}
		}
		cmd = exec.Command(path, args...)
	} else if path, err := exec.LookPath("kdialog"); err == nil {
		args := []string{"--title", title}
		if folders {
			args = append(args, "--getexistingdirectory", ".")
		} else {
			args = append(args, "--multiple", "--separate-output", "--getopenfilename", ".", strings.Join(patterns, " "))
		}
		cmd = exec.Command(path, args...)
	} else {
		return nil, errors.New("没有可用的文件选择器，请安装 zenity 或 kdialog")
	}
	out, err := cmd.Output()
	if err != nil {
		// Both tools exit with 1 when the dialog is dismissed.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, errors.New("已取消选择")
		}
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), separator) {
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("已取消选择")
	}
	return paths, nil
}

// Fonts 列出系统已安装的字体族（fontconfig）。
func Fonts() []string {
	out, err := exec.Command("fc-list", "--format", "%{family[0]}\n").Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var fonts []string
	for _, family := range strings.Split(string(out), "\n") {
		family = strings.TrimSpace(family)
		if family != "" && !seen[family] {
			seen[family] = true
			fonts = append(fonts, family)
		}
	}
	sort.Strings(fonts)
	return fonts
}

// FontData 读取字体族对应的字体文件。
func FontData(family string) ([]byte, error) {
	if family == "" {
		family = DefaultFont
	}
	out, err := exec.Command("fc-match", "--format", "%{file}", family).Output()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(strings.TrimSpace(string(out)))
}

// Suspend 让系统进入睡眠。
func Suspend() error { return exec.Command("systemctl", "suspend").Run() }

// Shutdown 关闭计算机。
func Shutdown() error { return exec.Command("systemctl", "poweroff").Run() }

// Beep 发出提示音；Linux 上没有通用的做法，不发声。
func Beep() {}

// RegisterFileMenu 在 Windows 上登记文件的右键菜单项；Linux 的文件管理器各不相同，不支持。
func RegisterFileMenu(exts []string, enable bool, label string) error {
	if !enable {
		return nil
	}
	return errors.New("此系统不支持在文件的右键菜单中添加项目")
}

// RegisterAppIdentity 写入桌面项（~/.local/share/applications），任务栏与系统媒体控件据此显示名称和图标。
func RegisterAppIdentity(id, name, iconPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".local", "share", "applications")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entry := "[Desktop Entry]\nType=Application\nName=" + name + "\nExec=\"" + exe + "\" %F\nIcon=" + iconPath + "\nCategories=AudioVideo;Audio;Player;\nStartupWMClass=" + id + "\nTerminal=false\n"
	file := filepath.Join(dir, id+".desktop")
	if current, _ := os.ReadFile(file); string(current) == entry {
		return nil
	}
	return os.WriteFile(file, []byte(entry), 0o644)
}

// 以下按窗口句柄工作的函数在 Linux 上由 CEF 宿主负责，这里不做任何事。

func IsVisible(hwnd uintptr) bool                      { return hwnd != 0 }
func ExcludeFromCapture(hwnd uintptr, exclude bool)    {}
func StyleWindow(hwnd uintptr)                         {}
func ShowWindowAsync(hwnd uintptr, show bool)          {}
func AllowForeground()                                 {}
func FocusContent(hwnd uintptr)                        {}
func IsIconic(hwnd uintptr) bool                       { return false }
func IsForegroundUncovered(hwnd, overlay uintptr) bool { return false }
