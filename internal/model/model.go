// Package model 定义各模块共享的数据结构。
package model

// Track 是一首曲目的描述，由音源插件提供。
type Track struct {
	Source             string            `json:"source"`   // 已注册的 scheme，旧扩展默认使用插件 ID
	ID                 string            `json:"id"`       // 插件内唯一 ID
	Title              string            `json:"title"`    // 标题
	Artist             string            `json:"artist"`   // 艺术家
	Album              string            `json:"album"`    // 专辑
	Duration           float64           `json:"duration"` // 时长（秒）
	Cover              string            `json:"cover"`    // 封面：embedded:<path> | file:<path> | http(s)://...
	LargeCover         string            `json:"largeCover,omitempty"`
	CoverExpires       float64           `json:"coverExpires,omitempty"`
	LargeCoverExpires  float64           `json:"largeCoverExpires,omitempty"`
	CoverHeaders       map[string]string `json:"coverHeaders,omitempty"`
	LargeCoverHeaders  map[string]string `json:"largeCoverHeaders,omitempty"`
	CoverProvider      string            `json:"coverProvider,omitempty"`
	LargeCoverProvider string            `json:"largeCoverProvider,omitempty"`
	InfoReady          bool              `json:"infoReady,omitempty"`
	MetadataReady      bool              `json:"metadataReady,omitempty"`
	InfoRevision       uint64            `json:"infoRevision,omitempty"`
	Extra              map[string]any    `json:"extra,omitempty"`
}

// Key 返回全局唯一键。
func (t Track) Key() string { return t.Source + "\x00" + t.ID }

// Sidebar 是扩展在侧栏维护的分区：标题、条目与「+」按钮都由扩展的 getSidebar 给出。
type Sidebar struct {
	Title  string        `json:"title"`
	CanAdd bool          `json:"canAdd"`
	Items  []SidebarItem `json:"items"`
}

type SidebarItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TrackList 是扩展处理侧栏条目点击后交给主界面显示的歌曲列表。
type TrackList struct {
	Title string `json:"title"`
	// 标题下方的一行，完全由扩展决定：任意多个「图标 + 文字」，也可以不给。
	Details []ListDetail `json:"details"`
	Tracks  []Track      `json:"tracks"`
}

type ListDetail struct {
	Icon string `json:"icon"` // RemixIcon 码位，可省略
	Text string `json:"text"`
}

// Action 是插件提供的右键菜单项。
type Action struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Icon        string   `json:"icon"` // RemixIcon 码位（十六进制，如 "ED8A"）
	Scenes      []string `json:"scenes,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Selection   string   `json:"selection,omitempty"`
	Children    []Action `json:"children,omitempty"`
	Dynamic     bool     `json:"dynamic,omitempty"`
	Separator   bool     `json:"separator,omitempty"` // 侧栏条目菜单中的分隔线
}

// Playlist 是用户自建歌单。
type Playlist struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Tracks []Track `json:"tracks"`
}

// StreamInfo 由插件 getStream 返回。
type StreamInfo struct {
	Path    string            `json:"path"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Format  string            `json:"format"` // 可选：mp3 / flac / ogg / wav
}

// DownloadInfo 由插件 getDownload 返回。
type DownloadInfo struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Ext     string            `json:"ext"`
	Lyrics  string            `json:"lyrics"`
}
