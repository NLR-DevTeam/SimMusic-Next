package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// 权限模型：
//
//   - 插件在 plugin.json 的 permissions 中声明需要的权限；未声明的权限永远不可用。
//   - 用户授权（grant）独立保存，可在扩展页逐项撤销 / 恢复；声明且已授权才生效。
//   - 内置插件默认授予其声明的全部权限；用户安装的插件在安装确认时授予。
//   - 插件自身数据目录（sim.plugin.dataDir）的读写、对话框、提示等无需任何权限。
const (
	PermNetwork        = "network" // sim.fetch；可用 manifest.hosts 限定域名
	PermClipboardWrite = "clipboard.write"
	PermFSRead         = "fs.read"  // 读取数据目录以外的文件与音频元数据
	PermFSWrite        = "fs.write" // 写入 / 删除数据目录以外的文件
	PermShell          = "shell"    // 系统集成：注册右键菜单、在资源管理器中打开、运行本机程序
	PermSidebar        = "sidebar"  // 在应用侧栏显示分组及歌单入口
	PermTrackMenu      = "trackMenu"
	PermSchemeExtend   = "scheme.extend"
	PermWebview        = "webview"  // 在主界面或独立窗口中显示扩展自己的网页
	PermPlayer         = "player"   // 播放控制：读取播放状态、队列、歌词与歌曲音频，执行播放页能做的全部操作并编排播放队列
	PermPlugins        = "plugin"   // 管理扩展：查看、重载，以及每次都需用户确认的安装、卸载、启用、停用
	PermPlayPage       = "playPage" // 提供播放页；页面的显示与隐藏完全由宿主控制，不需要 webview 权限
)

// PermInfo 描述一个权限，用于界面展示。
type PermInfo struct {
	Key   string
	Name  string
	Desc  string
	Icon  rune
	Risky bool
}

var permInfos = map[string]PermInfo{
	PermClipboardWrite: {PermClipboardWrite, "写入剪贴板", "将歌曲信息等文字复制到系统剪贴板", 0, false},
	PermNetwork:        {PermNetwork, "网络访问", "访问互联网（获取音源数据、歌词、封面等）", 0xEDCF, false},
	PermFSRead:         {PermFSRead, "读取文件", "读取扩展数据目录以外的文件，包括读取音频文件的元数据", 0xED0F, false},
	PermFSWrite:        {PermFSWrite, "写入文件", "在扩展数据目录以外创建、修改或删除文件，包括修改音频文件的元数据", 0xECDB, true},
	PermWebview:        {PermWebview, "网页界面", "在主界面内或独立窗口中显示扩展自己的网页", 0xF2C4, false},
	PermPlayer:         {PermPlayer, "播放控制", "读取播放状态、播放队列、歌词与频谱，获取队列中歌曲的音频地址或文件，控制播放、音量、倍速、播放队列与睡眠定时", 0xF00B, true},
	PermPlayPage:       {PermPlayPage, "播放页", "提供可在设置中选用的播放页，由主界面负责显示与隐藏", 0xF2C4, false},
	PermPlugins:        {PermPlugins, "扩展管理", "查看已安装的扩展并重新加载；安装、卸载、启用、停用扩展时，每次都会先征得您的确认", 0xF450, true},
	PermShell:          {PermShell, "系统集成", "修改系统右键菜单、在资源管理器中打开文件、运行本机程序等", 0xF1F6, true},
	PermSidebar:        {PermSidebar, "侧栏显示", "在应用侧栏显示此扩展的分组和歌单入口", 0xF011, false},
	PermTrackMenu:      {PermTrackMenu, "歌曲菜单", "在歌曲列表、播放详情和播放队列中提供附加操作", 0xEF79, false},
	PermSchemeExtend:   {PermSchemeExtend, "追加协议支持", "覆盖已有协议的音乐信息、下载处理，或追加歌曲菜单", 0xEC9D, false},
}

// Info 返回权限描述；未知权限也会返回一个可展示的条目。
func Info(perm string) PermInfo {
	if i, ok := permInfos[perm]; ok {
		return i
	}
	return PermInfo{Key: perm, Name: perm, Desc: "未知权限（不会被授予任何能力）", Icon: 0xEE59}
}

// Declared 返回插件声明的权限（已去重）。
func (m Manifest) Declared() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range m.Permissions {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func (m Manifest) declares(perm string) bool {
	for _, p := range m.Declared() {
		if p == perm {
			return true
		}
	}
	return false
}

// HasPermission 报告权限是否已声明且已授权。
func (p *Plugin) HasPermission(perm string) bool {
	return p.Manifest.declares(perm) && p.mgr.Granted(p.ID(), perm)
}

func (p *Plugin) permError(perm string) error {
	info := Info(perm)
	if !p.Manifest.declares(perm) {
		return fmt.Errorf("权限被拒绝：扩展未在 plugin.json 中声明「%s」(%s) 权限", info.Name, perm)
	}
	return fmt.Errorf("权限被拒绝：用户已撤销「%s」(%s) 权限", info.Name, perm)
}

// checkHost 校验网络请求的目标域名是否在 manifest.hosts 白名单内（未设置白名单时不限制）。
func (p *Plugin) checkHost(raw string) {
	if len(p.Manifest.Hosts) == 0 {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		p.throw(err)
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range p.Manifest.Hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == host || (strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:])) {
			return
		}
	}
	p.throw(errors.New("权限被拒绝：域名 " + host + " 不在扩展声明的 hosts 列表中"))
}

// ---------- 授权存储 ----------

func grantKey(id, perm string) string { return "plugin.grant." + id + "." + perm }

// Granted 报告用户是否授予了权限。内置插件默认授予。
func (m *Manager) Granted(id, perm string) bool {
	if v, ok := m.enabled.Get(grantKey(id, perm)).(bool); ok {
		return v
	}
	p := m.Get(id)
	return p != nil && p.Builtin
}

// SetGranted 授予 / 撤销权限。
func (m *Manager) SetGranted(id, perm string, on bool) {
	m.enabled.Set(grantKey(id, perm), on)
}

// GrantAll 授予插件声明的权限（安装确认时调用）；denied 为用户在安装界面关闭的权限。
func (m *Manager) GrantAll(man Manifest, denied ...string) {
	for _, perm := range man.Declared() {
		m.enabled.Set(grantKey(man.ID, perm), !slices.Contains(denied, perm))
	}
}

// ReadManifest 读取目录中的 plugin.json（用于安装前展示权限）。
func ReadManifest(dir string) (Manifest, error) {
	var man Manifest
	b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return man, errors.New("所选文件夹中没有 plugin.json")
	}
	if err := json.Unmarshal(b, &man); err != nil {
		return man, fmt.Errorf("plugin.json 格式错误：%w", err)
	}
	if err := validateManifest(man); err != nil {
		return man, err
	}
	if man.Main == "" {
		man.Main = "main.js"
	}
	return man, nil
}
