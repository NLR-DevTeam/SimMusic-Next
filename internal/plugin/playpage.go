package plugin

import (
	"errors"
	"regexp"
	"strings"
)

// PlayPage is a play page an extension offers in plugin.json. The user picks one of all offered
// pages in the settings; the host then shows it, in a browser of its own, in place of the built-in one.
type PlayPage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`    // 包内预览图
	URL         string `json:"url"`                  // 包内页面
	Background  string `json:"background,omitempty"` // 页面底色 #rrggbb，同时是窗口按钮区域的底色
	Dark        bool   `json:"dark,omitempty"`       // 深色页面：窗口按钮使用浅色图标
	// 页面自己绘制窗口按钮（经 simPlayer.window 与 close 操作），宿主不再把自己的按钮叠在页面上方。
	WindowButtons bool `json:"windowButtons,omitempty"`
}

var (
	playPageID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	playPageColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// PlayPageFile returns the package path of a page address, without its query and fragment.
func PlayPageFile(address string) string {
	if at := strings.IndexAny(address, "?#"); at >= 0 {
		address = address[:at]
	}
	return address
}

func validatePlayPages(pages []PlayPage) error {
	if len(pages) > 16 {
		return errors.New("playPages 最多声明 16 个播放页")
	}
	seen := map[string]bool{}
	for _, page := range pages {
		if !playPageID.MatchString(page.ID) || seen[page.ID] {
			return errors.New("playPages 的 id 必须是互不相同的安全标识")
		}
		seen[page.ID] = true
		if strings.TrimSpace(page.Name) == "" {
			return errors.New("播放页 " + page.ID + " 缺少 name")
		}
		if !safeArchivePath(PlayPageFile(page.URL)) {
			return errors.New("播放页 " + page.ID + " 的 url 必须是包内相对路径")
		}
		if page.Preview != "" && !safeArchivePath(page.Preview) {
			return errors.New("播放页 " + page.ID + " 的 preview 必须是包内相对路径")
		}
		if page.Background != "" && !playPageColor.MatchString(page.Background) {
			return errors.New("播放页 " + page.ID + " 的 background 应为 #rrggbb")
		}
	}
	return nil
}

// PlayPagesAllowed reports whether the extension may offer play pages now: it runs, and both offering
// play pages and playback control are declared and granted. The webview permission is not involved:
// the host alone creates, shows and hides a play page.
func (p *Plugin) PlayPagesAllowed() bool {
	return p.Running() && p.HasPermission(PermPlayPage) && p.HasPermission(PermPlayer)
}
