// Package webui serves the CEF interface. Extensions stay in plugin.Manager's Goja runtimes.
package webui

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"simmusic/internal/audio"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"simmusic/internal/core"
	"simmusic/internal/desklyric"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
	"simmusic/internal/winx"
)

//go:embed web
var files embed.FS

type UI struct {
	app                 *core.App
	desk                *desklyric.Window
	server              *http.Server
	url                 string
	origin              string
	prefix              string
	hwnd                atomic.Uint64
	pageToken           string
	playOpen            atomic.Bool
	mu                  sync.Mutex
	toasts              []string
	alerts              []string
	musicInvalidations  []string
	navigations         []map[string]string
	dragPaths           []string
	prompts             map[string]*promptRequest
	covers              map[string][]byte
	tracks              map[string]model.Track
	pendingInstall      string
	pendingInstallToken string
	pendingDevelopment  bool
	pendingManifest     plugin.Manifest
	quit                chan struct{}
	quitOnce            sync.Once
	lastHangDump        atomic.Int64
	webviews            webviewHost
}

func New(a *core.App, desk *desklyric.Window) *UI {
	u := &UI{app: a, desk: desk, covers: map[string][]byte{}, tracks: map[string]model.Track{}, quit: make(chan struct{})}
	a.OnToast = u.Toast
	a.OnAlert = u.Alert
	a.OnPrompt = u.Prompt
	a.OnConfirm = u.Confirm
	winx.Launch = u.launchFromHost
	a.OnSidebarOpen = func(pluginID, id string) {
		u.mu.Lock()
		u.navigations = append(u.navigations, map[string]string{"plugin": pluginID, "id": id})
		u.mu.Unlock()
	}
	a.OnMusicInvalidated = func(id string) { u.mu.Lock(); u.musicInvalidations = append(u.musicInvalidations, id); u.mu.Unlock() }
	a.Invalidate = u.Invalidate
	a.DialogOwner = func() uintptr { return uintptr(u.hwnd.Load()) }
	if a.Plugins != nil {
		a.Plugins.SetWebviewBackend(u)
	}
	if desk != nil {
		desk.OnControl = func(action string) {
			switch action {
			case "previous":
				a.Prev()
			case "toggle":
				a.TogglePlay()
			case "next":
				a.Next(false)
			case "mode":
				a.CycleMode()
			case "volumeUp", "volumeDown":
				// The wheel over the desktop lyrics is finer than in the main window: 2% a notch.
				step := 0.02
				if action == "volumeDown" {
					step = -step
				}
				a.SetVolume(max(0, min(1, a.Engine.Volume()+step)))
			case "close":
				a.State.Set("desk.visible", false)
			}
		}
	}
	return u
}

func (u *UI) Invalidate() {}
func (u *UI) Toast(msg string) {
	u.mu.Lock()
	u.toasts = append(u.toasts, msg)
	if len(u.toasts) > 30 {
		u.toasts = u.toasts[len(u.toasts)-30:]
	}
	u.mu.Unlock()
}
func (u *UI) Alert(msg string) {
	u.mu.Lock()
	u.alerts = append(u.alerts, msg)
	if len(u.alerts) > 30 {
		u.alerts = u.alerts[len(u.alerts)-30:]
	}
	u.mu.Unlock()
}
func (u *UI) Quit()                { u.quitOnce.Do(func() { u.clearInstall(); close(u.quit) }) }
func (u *UI) ShowWindow(show bool) { winx.ShowWindowAsync(uintptr(u.hwnd.Load()), show) }

func (u *UI) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		ln.Close()
		return "", err
	}
	u.prefix = "/" + hex.EncodeToString(token[:]) + "/"
	u.origin = "http://" + ln.Addr().String()
	u.url = u.origin + u.prefix
	web, _ := fs.Sub(files, "web")
	static := http.FileServer(http.FS(web))
	u.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != ln.Addr().String() || !strings.HasPrefix(r.URL.Path, u.prefix) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'none'")
		rel := strings.TrimPrefix(r.URL.Path, u.prefix)
		if rel == "extensionPackage" {
			u.preparePackage(w, r)
			return
		}
		if rel == "api" {
			if r.Method != "POST" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if !u.sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
			var q request
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				http.Error(w, "请求格式错误", 400)
				return
			}
			stopWatch := u.watchRequest(q.Action)
			defer stopWatch()
			v, err := u.call(q)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"result": v})
			return
		}
		if rel == "cover" {
			u.cover(w, r)
			return
		}
		if rel == "theme" {
			path := u.app.Cfg.String("list.themeImageSrc")
			if path == "" {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, path)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		r.URL.Path = "/" + rel
		static.ServeHTTP(w, r)
	})}
	if err := u.startExtServer(); err != nil {
		ln.Close()
		return "", err
	}
	go u.server.Serve(ln)
	go u.syncDesk()
	return u.url, nil
}

// sameOrigin reports whether a request comes from the interface page itself. With web security
// relaxed for extension webviews Chromium sends no Origin on same-origin requests; its
// Sec-Fetch-Site header, which pages cannot set, then tells the page's own requests apart.
func (u *UI) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == u.origin || origin == "" && r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

type request struct {
	Action string        `json:"action"`
	ID     string        `json:"id"`
	Plugin string        `json:"plugin"`
	Menu   string        `json:"menu"`
	Scene  string        `json:"scene"`
	Parent string        `json:"parent"`
	Key    string        `json:"key"`
	Text   string        `json:"text"`
	Value  any           `json:"value"`
	Index  int           `json:"index"`
	To     int           `json:"to"`
	Number float64       `json:"number"`
	DX     int           `json:"dx"`
	DY     int           `json:"dy"`
	Tracks []model.Track `json:"tracks"`
	Order  []string      `json:"order"`
	Words  []string      `json:"words"`
	Task   int           `json:"task"`
	Denied []string      `json:"denied"`
}

func (u *UI) remember(ts []model.Track) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, t := range ts {
		old, ok := u.tracks[t.Key()]
		// A reference of the same revision that lost its cover (a File is never indexed)
		// must not replace the complete record the cover endpoint serves from.
		if ok && (t.InfoRevision < old.InfoRevision || t.InfoRevision == old.InfoRevision && t.Cover == "" && old.Cover != "") {
			continue
		}
		u.tracks[t.Key()] = t
	}
}

func (u *UI) snapshot() any {
	s := u.app.Snapshot()
	u.remember(s.Queue)
	u.mu.Lock()
	msgs, alerts := u.toasts, u.alerts
	invalidations, navigations := u.musicInvalidations, u.navigations
	u.musicInvalidations, u.navigations = nil, nil
	u.toasts, u.alerts = nil, nil
	u.mu.Unlock()
	return map[string]any{"player": s, "sleep": u.app.Sleep(), "downloads": u.app.Downloads.Tasks(), "toasts": msgs, "alerts": alerts, "prompts": u.pendingPrompts(), "musicInvalidations": invalidations, "navigations": navigations, "deskVisible": u.app.State.Bool("desk.visible"), "windowMaximized": u.maximized(), "panel": u.activePanel()}
}

func (u *UI) catalog() any {
	var ps []any
	for _, p := range u.app.Plugins.All() {
		errText := ""
		if err := p.LoadErr(); err != nil {
			errText = err.Error()
		}
		ps = append(ps, map[string]any{"manifest": p.Manifest, "builtin": p.Builtin, "development": p.Development, "enabled": u.app.Plugins.IsEnabled(p.ID()), "running": p.Running(), "canDownload": p.DownloadAllowed(), "permissions": u.permissions(p), "error": errText, "canSearch": p.Has("search"), "sidebar": u.app.Sidebar(p.ID())})
	}
	lists := u.app.Lists.All()
	for _, l := range lists {
		u.remember(l.Tracks)
	}
	return map[string]any{"schemes": u.app.Plugins.Schemes(), "plugins": ps, "playlists": lists, "config": u.app.Cfg.All(), "version": core.Version, "downloadDir": u.app.DownloadDir(), "dataDir": u.app.DataDir, "runtime": runtime.Version() + " · " + runtime.GOOS + "/" + runtime.GOARCH, "lyricsShown": u.app.State.Get("lrcShow") != false, "eqBands": audio.EQBands, "eqPresets": audio.EQPresets}
}

func (u *UI) call(q request) (any, error) {
	a := u.app
	var p *plugin.Plugin
	if q.Plugin != "" {
		p = a.Plugins.Get(q.Plugin)
	}
	switch q.Action {
	case "snapshot":
		return u.snapshot(), nil
	case "dataFolder":
		winx.OpenPath(a.DataDir)
	case "logFolder":
		winx.OpenPath(filepath.Join(a.DataDir, "logs"))
	case "catalog":
		return u.catalog(), nil
	case "search":
		ts, err := a.Search(q.Text, q.Words, q.Plugin)
		u.remember(ts)
		return ts, err
	case "musicInfo":
		if q.Scene == "" {
			q.Scene = "list"
		}
		ts, err := a.GetMusicInfo(q.Tracks, q.Scene)
		if err != nil {
			return nil, a.MusicInfoFailed(q.Tracks, err)
		}
		u.remember(ts)
		return ts, nil
	case "clearMusicInfo":
		if q.Scene == "" || q.Scene == "queue" {
			q.Scene = "list"
		}
		ts, err := a.ClearMusicInfo(q.Tracks, q.Scene)
		u.mu.Lock()
		u.covers = map[string][]byte{}
		u.mu.Unlock()
		u.remember(ts)
		return ts, err
	case "play":
		a.PlayQueue(q.Tracks, q.Index)
	case "playIndex":
		a.PlayIndex(q.Index, true)
	case "toggle":
		a.TogglePlay()
	case "previous":
		a.Prev()
	case "next":
		a.Next(false)
	case "stop":
		a.Stop()
	case "seek":
		a.Seek(q.Number)
	case "volume":
		a.SetVolume(max(0, min(1, q.Number)))
	case "speed":
		a.SetSpeed(max(0.1, min(10, q.Number)))
	case "mode":
		return a.CycleMode(), nil
	case "appendQueue":
		a.AppendQueue(q.Tracks)
	case "playNext":
		a.PlayNextMany(q.Tracks)
	case "removeQueue":
		a.RemoveFromQueue(q.Index)
	case "clearQueue":
		a.ClearQueue()
	case "moveQueue":
		a.MoveQueue(q.Index, q.To)
	case "movePlaylist":
		a.Lists.Move(q.ID, q.Index, q.To)
	case "openFiles":
		paths, err := a.PickFiles("打开音乐", []string{"mp3", "flac", "ogg", "wav"})
		if err == nil {
			a.OpenFiles(paths)
		}
	case "dropFiles":
		u.mu.Lock()
		paths := u.dragPaths
		u.dragPaths = nil
		u.mu.Unlock()
		simex, refresh := a.DropFiles(paths, q.Text, q.Plugin, q.ID)
		result := map[string]any{"refresh": refresh}
		if simex != "" {
			file, err := os.Open(simex)
			if err != nil {
				return nil, err
			}
			defer file.Close()
			dir, manifest, err := plugin.PreparePackage(file)
			if err != nil {
				return nil, err
			}
			install, err := u.setInstall(dir, manifest, false)
			if err != nil {
				return nil, err
			}
			result["install"] = install
		}
		return result, nil
	case "fonts":
		return winx.Fonts(), nil
	case "audioDevices":
		devices, err := audio.OutputDevices()
		if err != nil {
			return nil, errors.New("无法读取音频输出设备")
		}
		return devices, nil
	case "sidebarAdd":
		a.SidebarAdd(q.Plugin)
	case "sidebarOpen":
		before := u.panelID()
		list, err := a.SidebarOpen(q.Plugin, q.Menu)
		if list != nil {
			u.remember(list.Tracks)
		} else if err == nil {
			// The extension answered the click with a page of its own instead of a song list.
			if panel, _ := u.activePanel().(map[string]string); panel != nil && panel["id"] != before && panel["plugin"] == q.Plugin {
				return map[string]any{"panel": panel["id"]}, nil
			}
		}
		return list, err
	case "panelLayout":
		u.panelLayout(q.ID, q.Value)
	case "panelClose":
		u.panelClose(q.ID)
	case "sidebarMenu":
		return a.SidebarMenu(q.Plugin, q.Menu)
	case "sidebarMenuAction":
		a.SidebarMenuAction(q.Plugin, q.Menu, q.Key)
	case "trackActions":
		if q.Scene == "" {
			q.Scene = "list"
		}
		return a.SongActions(q.Tracks, q.Scene, q.Parent)
	case "trackAction":
		if q.Scene == "" {
			q.Scene = "list"
		}
		return true, a.ExecuteSongAction(q.Tracks, q.Key, q.Scene)
	case "createPlaylist":
		return a.Lists.Create(q.Text), nil
	case "renamePlaylist":
		a.Lists.Rename(q.ID, q.Text)
	case "deletePlaylist":
		a.Lists.Delete(q.ID)
	case "addPlaylist":
		return a.Lists.Add(q.ID, q.Tracks...), nil
	case "removePlaylist":
		for _, t := range q.Tracks {
			a.Lists.Remove(q.ID, t.Key())
		}
		u.Toast(fmt.Sprintf("已移除 %d 首曲目", len(q.Tracks)))
	case "download":
		for _, t := range q.Tracks {
			if !a.CanDownload(t) {
				return nil, errors.New("此音源不支持下载：" + t.Title)
			}
		}
		a.Downloads.Enqueue(q.Tracks...)
	case "retryDownload":
		a.Downloads.Retry(q.Task)
	case "removeDownload":
		a.Downloads.Remove(q.Task)
	case "deleteDownload":
		a.Downloads.Delete(q.Task)
	case "revealDownload":
		if path := a.Downloads.Path(q.Task); path != "" {
			winx.RevealFile(path)
		}
	case "playDownload":
		if path := a.Downloads.Path(q.Task); path != "" {
			go a.OpenFiles([]string{path})
		}
	case "chooseDownloadDir":
		dir, err := a.PickFolder("选择下载目录")
		if err == nil {
			a.Cfg.Set("down.dir", dir)
		}
	case "chooseTheme":
		paths, err := a.PickFiles("选择主题图片", []string{"png", "jpg", "jpeg", "webp", "gif"})
		if err == nil && len(paths) > 0 {
			a.Cfg.Set("list.themeImageSrc", paths[0])
			a.Cfg.Set("list.themeImageType", "local")
		}
	case "config":
		if _, ok := core.Defaults[q.Key]; !ok {
			return nil, errors.New("未知设置")
		}
		if err := core.ValidateEQSetting(q.Key, q.Value); err != nil {
			return nil, err
		}
		a.Cfg.Set(q.Key, q.Value)
	case "sleep":
		if q.Number <= 0 || q.Number > 1440 {
			return nil, errors.New("定时时间应在 0 至 1440 分钟之间")
		}
		a.SetSleep(q.Number)
	case "cancelSleep":
		a.ExitSleep()
		u.Toast("已取消睡眠定时")
	case "indexSize":
		return a.MusicIndexSize(), nil
	case "clearIndex":
		a.ClearAllMusicInfo()
		u.mu.Lock()
		u.covers = map[string][]byte{}
		u.mu.Unlock()
		u.Toast("索引缓存已清理")
	case "cancelPending":
		a.CancelPending()
	case "sleepState":
		return a.Sleep(), nil
	case "pageReady":
		u.mu.Lock()
		u.pageToken = q.ID
		u.mu.Unlock()
		u.window(q.Action)
	case "pageLoading":
		u.mu.Lock()
		matches := q.ID == u.pageToken
		u.mu.Unlock()
		if matches {
			u.window(q.Action)
		}
	case "sleepPageReady":
		u.window(q.Action)
	case "desktopLyrics":
		a.State.Set("desk.visible", !a.State.Bool("desk.visible"))
	case "playPage":
		u.playOpen.Store(q.Value == true)
	case "lyricsShown":
		a.State.Set("lrcShow", q.Value == true)
	case "pluginPermissions":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		return u.permissions(p), nil
	case "trackMenuPermission":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		return true, p.SetTrackMenuGranted(q.Key, q.Value == true)
	case "pluginPermission":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		declared := false
		for _, key := range p.Manifest.Declared() {
			if key == q.Key {
				declared = true
			}
		}
		if !declared {
			return nil, errors.New("扩展未声明此权限")
		}
		a.Plugins.SetGranted(p.ID(), q.Key, q.Value == true)
		if q.Key == plugin.PermSchemeExtend {
			a.Plugins.RestartOrdered()
			a.InvalidateMusicResources(nil)
			go a.ReloadMenus()
		}
		if q.Key == plugin.PermSidebar {
			go a.ReloadMenus()
		}
		u.mu.Lock()
		u.covers = map[string][]byte{}
		u.mu.Unlock()
	case "pluginSettings":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		return map[string]any{"sections": a.PluginSettings(p), "values": p.Settings.All()}, nil
	case "pluginSetting":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		a.SetPluginSetting(p, q.Key, q.Value)
	case "pluginAction":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		a.PluginAction(p, q.Key, nil)
	case "pluginEnabled":
		if err := a.Plugins.SetEnabled(q.Plugin, q.Value == true); err != nil {
			return nil, err
		}
		a.InvalidateMusicResources(nil)
		go a.ReloadMenus()
	case "pluginOrder":
		if err := a.Plugins.SetOrder(q.Order); err != nil {
			return nil, err
		}
		a.InvalidateMusicResources(nil)
		go a.ReloadMenus()
	case "reloadPlugin":
		if err := a.Plugins.Reload(q.Plugin); err != nil {
			return nil, err
		}
		a.InvalidateMusicResources(nil)
		go a.ReloadMenus()
	case "removePlugin":
		if err := a.Plugins.Remove(q.Plugin); err != nil {
			return nil, err
		}
		a.InvalidateMusicResources(nil)
		go a.ReloadMenus()
	case "pluginLogs":
		if p == nil {
			return nil, errors.New("扩展不存在")
		}
		return p.Logs(), nil
	case "pluginFolder":
		winx.OpenPath(a.Plugins.UserDir())
	case "prepareInstall":
		u.clearInstall()
		dir, err := a.PickFolder("选择扩展目录")
		if err != nil || dir == "" {
			return nil, nil
		}
		manifest, err := plugin.ReadManifest(dir)
		if err != nil {
			return nil, err
		}
		return u.setInstall(dir, manifest, true)
	case "promptResult":
		u.resolvePrompt(q.ID, q.Text)
	case "cancelInstall":
		u.mu.Lock()
		matches := q.ID == u.pendingInstallToken
		u.mu.Unlock()
		if !matches {
			return nil, errors.New("扩展选择已改变")
		}
		u.clearInstall()
	case "installPlugin":
		u.mu.Lock()
		if q.ID == "" || q.ID != u.pendingInstallToken {
			u.mu.Unlock()
			return nil, errors.New("请重新选择扩展并确认权限")
		}
		dir := u.pendingInstall
		development, reviewed := u.pendingDevelopment, u.pendingManifest
		u.pendingInstall = ""
		u.pendingInstallToken = ""
		u.mu.Unlock()
		if dir == "" {
			return nil, errors.New("请先选择扩展并确认权限")
		}
		if !development {
			defer os.RemoveAll(dir)
		}
		current, err := plugin.ReadManifest(dir)
		if err != nil {
			return nil, err
		}
		if !plugin.SameManifest(current, reviewed) {
			return nil, errors.New("扩展声明已改变，请重新选择并确认权限")
		}
		if development {
			_, err = a.Plugins.LoadDevelopment(dir, q.Denied...)
		} else {
			_, err = a.Plugins.Install(dir, q.Denied...)
		}
		if err != nil {
			return nil, err
		}
		a.InvalidateMusicResources(nil)
		go a.ReloadMenus()
	case "window":
		u.window(q.Text)
	case "moveDesktopLyrics":
		if u.desk != nil {
			u.desk.Nudge(q.DX, q.DY)
		}
	default:
		return nil, fmt.Errorf("未知操作：%s", q.Action)
	}
	return true, nil
}

func (u *UI) permissions(p *plugin.Plugin) []any {
	var out []any
	for _, key := range p.Manifest.Declared() {
		row := map[string]any{"info": plugin.Info(key), "granted": p.HasPermission(key)}
		if key == plugin.PermTrackMenu {
			var menus []any
			var add func([]model.Action, string)
			add = func(items []model.Action, prefix string) {
				for _, item := range items {
					path := prefix + item.Key
					menus = append(menus, map[string]any{"key": path, "label": item.Label, "icon": item.Icon, "scenes": item.Scenes, "granted": p.TrackMenuPreference(path), "available": p.TrackMenuGranted(path)})
					add(item.Children, path+"/")
				}
			}
			add(p.MenuDeclarations(), "")
			row["menus"] = menus
		}
		out = append(out, row)
	}
	return out
}

func (u *UI) cover(w http.ResponseWriter, r *http.Request) {
	k := r.URL.Query().Get("key")
	u.mu.Lock()
	t, known := u.tracks[k]
	u.mu.Unlock()
	if !known {
		http.NotFound(w, r)
		return
	}
	scene := r.URL.Query().Get("scene")
	if scene != "play" {
		scene = "list"
	}
	expires := t.CoverExpires
	if scene == "play" && t.LargeCover != "" {
		expires = t.LargeCoverExpires
	}
	if expires > 0 && expires <= float64(time.Now().UnixMilli())/1000 {
		if fresh, err := u.app.GetMusicInfo([]model.Track{t}, scene); err == nil {
			t = fresh[0]
			u.remember(fresh)
		}
	} else if t.Cover == "" {
		// The record holds no cover reference: ask the extension again.
		if fresh, err := u.app.GetMusicInfo([]model.Track{t}, "list"); err == nil {
			t = fresh[0]
			u.remember(fresh)
		}
	}
	if scene == "play" && t.LargeCover != "" {
		t.Cover, t.CoverHeaders = t.LargeCover, t.LargeCoverHeaders
		t.CoverProvider = t.LargeCoverProvider
	}
	cacheKey := fmt.Sprintf("%s\x00%s\x00%d\x00%s", k, scene, t.InfoRevision, t.Cover)
	u.mu.Lock()
	b, exists := u.covers[cacheKey]
	u.mu.Unlock()
	if !exists {
		b = u.app.CoverBytes(t)
		u.mu.Lock()
		if len(u.covers) >= 512 {
			for key := range u.covers {
				delete(u.covers, key)
				break
			}
		}
		if len(b) > 0 {
			u.covers[cacheKey] = b
		}
		u.mu.Unlock()
	}
	// Revisioned local/File covers can be reused for this run. Expiring web
	// covers never outlive their plugin's declared lifetime in Chromium's cache.
	maxAge := 31536000
	expires = t.CoverExpires
	if scene == "play" && t.LargeCover != "" {
		expires = t.LargeCoverExpires
	}
	if expires > 0 {
		maxAge = max(0, min(maxAge, int(expires-float64(time.Now().UnixMilli())/1000)))
	} else if strings.HasPrefix(t.Cover, "http://") || strings.HasPrefix(t.Cover, "https://") {
		maxAge = 0
	}
	if r.URL.Query().Get("rev") != fmt.Sprint(t.InfoRevision) {
		maxAge = 0
	}
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", maxAge))
	if len(b) == 0 {
		// The placeholder must never be kept under a cover address: a later request
		// for the same revision would show it for good instead of the real cover.
		w.Header().Set("Cache-Control", "no-store")
		r.URL.Path = "/assets/placeholder.png"
		web, _ := fs.Sub(files, "web")
		http.FileServer(http.FS(web)).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Write(b)
}
