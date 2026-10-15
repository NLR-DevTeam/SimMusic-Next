// Package core 汇总播放队列、插件、歌单、睡眠定时与下载等应用逻辑，供 UI 调用。
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"simmusic/internal/audio"
	"simmusic/internal/config"
	"simmusic/internal/lyrics"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
	"simmusic/internal/winx"
)

const Version = "0.1.0-demo"

// 播放模式（与旧版 loop 配置一致：列表循环 → 单曲循环 → 随机播放）
const (
	ModeLoop    = 0 // 列表循环
	ModeRepeat  = 1 // 单曲循环
	ModeShuffle = 2 // 随机
)

// Defaults 为主程序设置的默认值。
var Defaults = map[string]any{
	// 通用
	"general.noBackground": false,
	"general.font":         "",
	// 歌单界面
	"list.showLocate":     true,
	"list.themedButton":   false,
	"list.themeImage":     false,
	"list.themeImageType": "cover",
	"list.themeImageSrc":  "",
	"list.themeImageBlur": false,
	"list.leftBarWidth":   200.0,
	// 播放界面
	"play.page":         "", // 所选播放页「扩展 id/播放页 id」；空为内置播放页
	"ctl.quickReplay":   false,
	"ctl.fade":          true,
	"audio.device":      "", // 输出设备 ID；空为系统默认
	"eq.enabled":        true,
	"eq.gains":          []any{0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0},
	"eq.profile":        "basic",
	"eq.filters":        audio.BasicEQFilters(nil),
	"sleep.extendToEnd": true,
	"sleep.action":      "none",
	// 歌词视图
	"lrc.multiLang": true,
	"lrc.perWord":   lyrics.WordsSource, // 逐字歌词解析：source 按原歌词处理 / extend 扩展到全曲 / all 所有歌曲
	"lrc.transSize": 0.8,
	// 桌面歌词
	"dl.rememberState": true,
	"dl.compact":       false,
	"dl.hideCapture":   true,
	"dl.autoClose":     true,
	"dl.showTrans":     false,
	"dl.color":         "#1E9FFF",
	"dl.border":        true,
	"dl.borderColor":   "#1672B8",
	"dl.size":          30.0,
	"dl.width":         700.0,
	"dl.center":        false,
	// 曲目下载
	"down.parallel":    "3",
	"down.nameFormat":  "[title] - [artist]",
	"down.writeTitle":  true,
	"down.writeArtist": true,
	"down.writeCover":  true,
	"down.lyricMode":   "embed",
	"down.dir":         "",
}

// App 是应用核心。
type App struct {
	DataDir string
	Cfg     *config.Store // 用户设置
	State   *config.Store // 运行状态（音量、队列等）
	Engine  *audio.Engine
	Plugins *plugin.Manager
	Lists   *Playlists

	// UI 回调
	Invalidate         func()
	OnToast            func(msg string)
	OnAlert            func(msg string)
	OnPrompt           func(title, placeholder, value string) (string, error)
	OnMusicInvalidated func(pluginID string)
	OnConfirm          func(text string) (bool, error)
	OnSidebarOpen      func(pluginID, id string)
	OnQuit             func()
	OnShowWindow       func()
	OnSongMenu         func()
	DialogOwner        func() uintptr

	mu        sync.Mutex
	queue     []model.Track
	index     int
	mode      int
	shuffle   []int // 正常队列的索引排列；队列中每首歌只出现一次。
	lyricsRaw string
	lyricLns  []lyrics.Line
	// The word-by-word reading of lyricsRaw, kept until the text, the settings or the duration change.
	words struct {
		raw, mode string
		multi     bool
		duration  float64
		lines     []lyrics.WordLine
	}
	lyricKey    string
	infoLoading map[string]bool // songs of the queue whose information is being read for a player display
	loadGen     int
	loading     int // loadGen of the audio load in flight, zero when idle
	// While a track the user switched to is opening, the player shows that track: its start
	// position, and whether it will play once ready (play / pause pressed meanwhile only flips this).
	switching int
	wantPlay  bool
	loadPos   float64
	failCount int
	prefetch  *playPrefetch

	menuMu   sync.Mutex
	sidebars map[string]*model.Sidebar

	sleep     SleepState
	Downloads *Downloader
	infoOnce  sync.Once
	infoIndex *musicIndex
}

// SleepState 描述睡眠定时器状态。
type SleepState struct {
	Until        time.Time // 计时结束时刻
	WaitTrackEnd bool      // 已到时，等待当前曲目结束
	Pending      string    // 即将执行的系统操作（sleep / shutdown）
	PendingAt    time.Time
}

func New(dataDir string) (*App, error) {
	a := &App{DataDir: dataDir}
	a.Cfg = config.Open(filepath.Join(dataDir, "config.json"), Defaults)
	audio.SetOutputDevice(a.Cfg.String("audio.device"))
	a.State = config.Open(filepath.Join(dataDir, "state.json"), map[string]any{"volume": 0.8, "mode": 0.0})
	if a.Cfg.Get("dl.onStartup") != nil {
		a.Cfg.Delete("dl.onStartup")
	}
	if !a.Cfg.Bool("dl.rememberState") {
		a.State.Set("desk.visible", false)
	}
	a.Lists = OpenPlaylists(filepath.Join(dataDir, "playlists.json"))
	eng, err := audio.New()
	if err != nil {
		return nil, fmt.Errorf("音频设备初始化失败: %w", err)
	}
	a.Engine = eng
	eng.SetVolume(a.State.Float("volume"))
	eng.SetFade(a.Cfg.Bool("ctl.fade"))
	a.applyEQ()
	eng.OnEnded = a.onEnded
	a.mode = a.State.Int("mode")
	a.Plugins = plugin.NewManager(filepath.Join(dataDir, "plugin-data"), filepath.Join(dataDir, "plugins"), a.State, a)
	a.Downloads = newDownloader(a)

	a.Cfg.OnChange(func(key string, v any) {
		switch {
		case key == "audio.device":
			audio.SetOutputDevice(a.Cfg.String("audio.device"))
		case key == "ctl.fade":
			eng.SetFade(a.Cfg.Bool("ctl.fade"))
		case strings.HasPrefix(key, "eq."):
			a.applyEQ()
		case key == "lrc.multiLang":
			a.mu.Lock()
			a.lyricLns = lyrics.Parse(a.lyricsRaw, a.Cfg.Bool("lrc.multiLang"))
			a.mu.Unlock()
		}
		a.redraw()
	})
	go a.ticker()
	return a, nil
}

func (a *App) applyEQ() {
	if a.Cfg.String("eq.profile") == "json" {
		var filters []audio.EQFilter
		if a.Cfg.Unmarshal("eq.filters", &filters) && a.Engine.EQ().SetFilters(a.Cfg.Bool("eq.enabled"), filters) == nil {
			return
		}
	}
	var gains []float64
	a.Cfg.Unmarshal("eq.gains", &gains)
	if len(gains) != len(audio.EQBands) {
		gains = make([]float64, len(audio.EQBands))
	}
	a.Engine.EQ().Set(a.Cfg.Bool("eq.enabled"), gains)
}

func ValidateEQSetting(key string, value any) error {
	b, _ := json.Marshal(value)
	switch key {
	case "eq.profile":
		profile, ok := value.(string)
		if !ok || profile != "basic" && profile != "json" {
			return errors.New("未知均衡器模式")
		}
	case "eq.gains":
		var gains []float64
		if json.Unmarshal(b, &gains) != nil || len(gains) != len(audio.EQBands) {
			return errors.New("基础均衡器需要 10 个数值")
		}
		return audio.ValidateEQFilters(audio.BasicEQFilters(gains))
	case "eq.filters":
		var filters []audio.EQFilter
		if len(b) == 0 || b[0] != '[' || json.Unmarshal(b, &filters) != nil {
			return errors.New("JSON 均衡器配置须为 F、G、Q 对象数组")
		}
		var objects []map[string]any
		if json.Unmarshal(b, &objects) != nil {
			return errors.New("均衡器配置须为对象数组")
		}
		for _, item := range objects {
			for _, key := range []string{"F", "G", "Q"} {
				if _, ok := item[key].(float64); !ok {
					return errors.New("每个滤波器须包含数值 F、G、Q")
				}
			}
		}
		return audio.ValidateEQFilters(filters)
	}
	return nil
}

func (a *App) redraw() {
	if a.Invalidate != nil {
		a.Invalidate()
	}
}

// ---------- plugin.Host 实现 ----------

func (a *App) Toast(msg string) {
	log.Println("toast:", msg)
	if a.OnToast != nil {
		a.OnToast(msg)
	}
}

// Alert shows text of unbounded length (errors, names, extension text) in a dialog.
func (a *App) Alert(msg string) {
	log.Println("alert:", msg)
	if a.OnAlert != nil {
		a.OnAlert(msg)
	}
}

func (a *App) Prompt(title, placeholder, value string) (string, error) {
	if a.OnPrompt == nil {
		return "", errors.New("当前界面不支持输入对话框")
	}
	return a.OnPrompt(title, placeholder, value)
}

// fail keeps the unbounded error text on its own line.
func (a *App) fail(summary string, err error) { a.Alert(summary + "：\n" + err.Error()) }

func (a *App) PickFolder(title string) (string, error) {
	paths, err := winx.PickPaths(a.dialogOwner(), title, true, nil)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", errors.New("未选择目录")
	}
	return paths[0], nil
}

func (a *App) PickFiles(title string, exts []string) ([]string, error) {
	return winx.PickPaths(a.dialogOwner(), title, false, exts)
}

func (a *App) dialogOwner() uintptr {
	if a.DialogOwner != nil {
		return a.DialogOwner()
	}
	return 0
}

// OpenFiles 处理「打开方式」传入的文件。
func (a *App) OpenFiles(paths []string) {
	if len(paths) == 0 {
		return
	}
	for _, p := range a.Plugins.Running() {
		var ts []model.Track
		if err := p.CallInto(time.Minute, &ts, "openFiles", paths); err != nil || len(ts) == 0 {
			continue
		}
		if err := a.Plugins.ValidateTracks(p, ts); err != nil {
			a.fail("扩展「"+p.Manifest.Name+"」无法打开文件", err)
			continue
		}
		a.PlayQueue(ts, 0)
		return
	}
	a.Toast("无法打开文件")
}

// ---------- 封面 ----------

// CoverBytes 获取曲目封面原始数据。
func (a *App) CoverBytes(t model.Track) []byte {
	c := t.Cover
	p := a.resourcePlugin(t.Source, t.CoverProvider)
	var resource model.Resource
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &resource)
	resource.Headers = t.CoverHeaders
	if p == nil || p.ValidateResource(resource) != nil {
		return nil
	}
	switch {
	case strings.HasPrefix(c, "embedded:"):
		return audio.ReadCover(strings.TrimPrefix(c, "embedded:"))
	case strings.HasPrefix(c, "file:"):
		b, _ := os.ReadFile(strings.TrimPrefix(c, "file:"))
		return b
	case strings.HasPrefix(c, "http://"), strings.HasPrefix(c, "https://"):
		b, _ := resourceBytes(p, resource)
		return b
	}
	return nil
}

// ---------- 播放队列 ----------

type Snapshot struct {
	Queue    []model.Track
	Index    int
	Mode     int
	Current  *model.Track
	Playing  bool
	Pos, Dur float64
	Lyrics   []lyrics.Line
	Words    []lyrics.WordLine // word-by-word lyrics; nil when the song has none
	Volume   float64
	Speed    float64
	Loading  bool // the current track's audio is still being opened
}

func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	s := Snapshot{Queue: a.activeQueueLocked(), Index: a.activeIndexLocked(), Mode: a.mode, Lyrics: append([]lyrics.Line(nil), a.lyricLns...), Loading: a.loading != 0 && a.loading == a.loadGen}
	if a.index >= 0 && a.index < len(a.queue) {
		t := a.queue[a.index]
		s.Current = &t
	}
	switching, wantPlay, loadPos := s.Loading && a.switching == a.loadGen, a.wantPlay, a.loadPos
	a.mu.Unlock()
	s.Playing = a.Engine.Playing()
	s.Pos, s.Dur = a.Engine.Position()
	if s.Loading {
		s.Playing = wantPlay
	}
	if switching && s.Current != nil {
		// The engine still holds the previous, now silent track.
		s.Pos, s.Dur = loadPos, s.Current.Duration
	} else if !a.Engine.Loaded() && s.Current != nil {
		s.Pos = a.State.Float("position")
	}
	if s.Dur == 0 && s.Current != nil {
		s.Dur = s.Current.Duration
	}
	s.Words = a.lyricWords(s.Dur)
	s.Volume = a.Engine.Volume()
	s.Speed = a.Engine.Speed()
	return s
}

// uniqueTracks drops repeated songs, keeping the first occurrence; at is followed to the kept position.
func uniqueTracks(tracks []model.Track, at int) ([]model.Track, int) {
	seen := map[string]int{}
	out := make([]model.Track, 0, len(tracks))
	kept := at
	for i, t := range tracks {
		position, ok := seen[t.Key()]
		if !ok {
			position = len(out)
			seen[t.Key()] = position
			out = append(out, t)
		}
		if i == at {
			kept = position
		}
	}
	return out, kept
}

// PlayQueue 用 tracks 替换队列并播放 idx。队列不含重复歌曲。
func (a *App) PlayQueue(tracks []model.Track, idx int) {
	if len(tracks) == 0 {
		a.ClearQueue()
		return
	}
	tracks, idx = uniqueTracks(tracks, idx)
	a.mu.Lock()
	a.queue = tracks
	a.shuffle = newShuffle(len(tracks))
	a.index = -1
	if a.mode == ModeShuffle {
		if idx < 0 {
			idx = 0
		} else {
			idx = a.shuffleIndexLocked(idx)
		}
	}
	a.mu.Unlock()
	a.PlayIndex(max(idx, 0), true)
}

// PlayNext 将曲目插入到当前曲目之后。
func (a *App) PlayNext(t model.Track) {
	a.PlayNextMany([]model.Track{t})
}

// AppendQueue 追加到队列末尾；已在队列中的歌曲不再重复添加。
func (a *App) AppendQueue(ts []model.Track) {
	added, started := a.appendQueue(ts)
	switch {
	case added == 0:
		a.Toast("歌曲已在队列中")
	case !started:
		a.Toast(fmt.Sprintf("已添加 %d 首歌曲", added))
	}
}

// appendQueue appends without a notice: how many songs were added, and whether they started an empty queue.
func (a *App) appendQueue(ts []model.Track) (int, bool) {
	ts, _ = uniqueTracks(ts, 0)
	a.mu.Lock()
	queued := map[string]bool{}
	for _, t := range a.queue {
		queued[t.Key()] = true
	}
	added := ts[:0:0]
	for _, t := range ts {
		if !queued[t.Key()] {
			added = append(added, t)
		}
	}
	empty := len(a.queue) == 0
	start := len(a.queue)
	a.queue = append(a.queue, added...)
	for _, i := range newShuffle(len(added)) {
		a.shuffle = append(a.shuffle, start+i)
	}
	a.mu.Unlock()
	if len(added) == 0 {
		return 0, false
	}
	if empty {
		a.PlayIndex(0, true)
		return len(added), true
	}
	a.saveQueue()
	a.redraw()
	return len(added), false
}

// removeAtLocked deletes a normal-list position; the shuffle order and the current
// index keep pointing at the same songs. The caller handles removal of the current song.
func (a *App) removeAtLocked(normal int) {
	a.queue = append(a.queue[:normal:normal], a.queue[normal+1:]...)
	order := make([]int, 0, len(a.queue))
	for _, j := range a.shuffle {
		if j == normal {
			continue
		}
		if j > normal {
			j--
		}
		order = append(order, j)
	}
	a.shuffle = order
	if normal < a.index {
		a.index--
	}
}

func (a *App) RemoveFromQueue(i int) {
	a.mu.Lock()
	if i < 0 || i >= len(a.queue) {
		a.mu.Unlock()
		return
	}
	normal := a.normalIndexLocked(i)
	cur := normal == a.index
	a.removeAtLocked(normal)
	if cur {
		a.index = -1
		a.loadGen++
	}
	n := len(a.queue)
	a.mu.Unlock()
	if cur {
		if n == 0 {
			a.Engine.Unload()
			a.mu.Lock()
			a.index = -1
			a.mu.Unlock()
		} else {
			a.PlayIndex(min(i, n-1), a.Engine.Playing())
		}
	}
	a.saveQueue()
	a.redraw()
}

func (a *App) ClearQueue() {
	a.Engine.Unload()
	a.mu.Lock()
	a.queue, a.index, a.shuffle = nil, -1, nil
	a.loadGen++
	a.lyricLns, a.lyricsRaw = nil, ""
	a.mu.Unlock()
	a.saveQueue()
	a.redraw()
}

// PlayIndex 加载并播放队列中的第 i 首。
func (a *App) PlayIndex(i int, autoplay bool) {
	a.playIndexAt(i, autoplay, 0)
}

func (a *App) playIndexAt(i int, autoplay bool, startPos float64) {
	a.startTrackAt(i, autoplay, startPos, false)
}

func (a *App) startTrackAt(i int, autoplay bool, startPos float64, normal bool) {
	a.mu.Lock()
	if i < 0 || i >= len(a.queue) {
		a.mu.Unlock()
		return
	}
	if !normal {
		i = a.normalIndexLocked(i)
	}
	a.index = i
	t := a.queue[i]
	prepared := a.prefetch
	if prepared != nil && (prepared.track.Key() != t.Key() || prepared.generation != a.loadGen) {
		prepared = nil
	}
	a.prefetch = nil
	a.loadGen++
	gen := a.loadGen
	a.loading, a.switching = gen, gen
	a.wantPlay, a.loadPos = autoplay, startPos
	a.lyricLns, a.lyricsRaw = nil, ""
	a.mu.Unlock()
	// The previous song falls silent at once: the player already shows the new one.
	a.Engine.SetPlaying(false)
	a.saveQueue()
	a.redraw()

	go func() {
		defer a.loaded(gen)
		fresh, raw, err := a.loadPreparedTrack(t, a.playIntent(gen), prepared)
		a.mu.Lock()
		stale := gen != a.loadGen
		a.mu.Unlock()
		if stale {
			return
		}
		if err != nil {
			// Nothing of the song shown is loaded; the previous one must not come back on "play".
			a.Engine.Unload()
			a.mu.Lock()
			a.failCount++
			a.mu.Unlock()
			a.failPlayback(fresh, err, gen)
			return
		}
		a.mu.Lock()
		// The queue may have changed while the song was opening (another one taken out or moved): the
		// current index follows the song through that, the position it was started at does not.
		if gen != a.loadGen || a.index < 0 || a.index >= len(a.queue) || a.queue[a.index].Key() != t.Key() {
			a.mu.Unlock()
			return
		}
		a.failCount = 0
		a.queue[a.index] = fresh
		a.lyricsRaw, a.lyricLns = raw, lyrics.Parse(raw, a.Cfg.Bool("lrc.multiLang"))
		a.mu.Unlock()
		if startPos > 0 {
			a.Engine.Seek(startPos)
		}
		a.redraw()
		a.saveQueue()
	}()
}

func (a *App) loaded(gen int) {
	a.mu.Lock()
	if a.loading == gen {
		a.loading = 0
	}
	a.mu.Unlock()
	a.redraw()
}

// playIntent is asked once the audio of load gen is open: play or pause may have been pressed meanwhile.
func (a *App) playIntent(gen int) func() bool {
	return func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return gen == a.loadGen && a.wantPlay
	}
}

// restoreCurrent brings back the last session's track: its information and lyrics first,
// then the audio, opened paused at the saved position so it is ready the moment the user plays.
func (a *App) restoreCurrent(t model.Track, gen int) {
	defer a.loaded(gen)
	info, fresh, err := a.resolveMusicInfo(t, "play")
	if err != nil {
		a.sourceError(t, "歌曲信息恢复失败", err)
		a.showCachedLyrics(t, gen)
		a.Alert(infoFailed)
		return
	}
	raw := a.musicLyrics(t.Source, info.Lyrics)
	lines := lyrics.Parse(raw, a.Cfg.Bool("lrc.multiLang"))
	a.mu.Lock()
	current := gen == a.loadGen && a.index >= 0 && a.index < len(a.queue) && a.queue[a.index].Key() == t.Key()
	if current {
		a.queue[a.index] = fresh
		a.lyricsRaw, a.lyricLns = raw, lines
	}
	a.mu.Unlock()
	a.redraw()
	if !current || info.Playback.Empty() {
		return
	}
	p := a.resourcePlugin(t.Source, info.Playback.Provider)
	if p == nil || p.ValidateResource(info.Playback) != nil {
		return
	}
	if err := a.Engine.LoadWhen(audio.Source{Path: info.Playback.Path, URL: info.Playback.URL, Headers: info.Playback.Headers, Format: info.Format}, a.playIntent(gen)); err != nil {
		a.failPlayback(fresh, playFailure{audioReason(err), err}, gen)
		return
	}
	// The position may have been moved while the audio was opening. Once the engine
	// holds it, the saved value is spent and must not be applied to a later track.
	if position := a.State.Float("position"); position > 0 {
		a.Engine.Seek(position)
	}
	a.State.Set("position", 0)
}

// playFailure pairs a fixed reason for the user with the underlying error. The reason never
// carries ids, names or paths; those only reach the log.
type playFailure struct {
	reason string
	err    error
}

func (f playFailure) Error() string { return f.reason }

func audioReason(err error) string {
	var missing *os.PathError
	switch {
	case errors.As(err, &missing):
		return "音频文件不存在或无法读取"
	case strings.HasPrefix(err.Error(), "HTTP "):
		return "音频地址无法访问"
	case strings.HasPrefix(err.Error(), "不支持的音频格式"):
		return "不支持的音频格式"
	}
	return "音频无法打开或解码"
}

// failPlayback tells the user that the song of load gen cannot be played and asks whether to go on to the
// next one: confirming does, cancelling leaves the player stopped on this song.
func (a *App) failPlayback(t model.Track, err error, gen int) {
	reason := "未知错误"
	var failure playFailure
	if errors.As(err, &failure) {
		reason = failure.reason
		if failure.err != nil {
			err = failure.err
		}
	}
	a.sourceError(t, "歌曲播放失败，"+reason, err)
	title := t.Title
	if title == "" {
		title = "未知歌曲"
	}
	text := "歌曲播放失败：" + title + "\n原因：" + reason
	// The song is no longer loading while the question stands.
	a.loaded(gen)
	next, askErr := a.Confirm(text + "\n是否切换到下一曲？")
	if askErr != nil {
		a.Alert(text)
		return
	}
	a.mu.Lock()
	// Answered after the user moved on to another song by themself: nothing is left to decide.
	current := gen == a.loadGen
	if current && !next {
		a.wantPlay = false
	}
	a.mu.Unlock()
	if current && next {
		a.Next(false)
	}
}

func (a *App) loadPreparedTrack(t model.Track, play func() bool, prepared *playPrefetch) (model.Track, string, error) {
	if a.Plugins.Scheme(t.Source) == nil {
		return t, "", playFailure{"音源扩展未启用", nil}
	}
	info, fresh, err := a.preparedMusicInfo(t, prepared)
	if err != nil {
		return t, "", playFailure{"扩展未能获取歌曲信息", err}
	}
	if info.Playback.Empty() {
		return fresh, "", playFailure{"扩展未返回音频文件", nil}
	}
	p := a.resourcePlugin(t.Source, info.Playback.Provider)
	if p == nil {
		return fresh, "", playFailure{"提供音频的扩展未启用", nil}
	}
	if err := p.ValidateResource(info.Playback); err != nil {
		return fresh, "", playFailure{"扩展无权访问该音频", err}
	}
	if err := a.Engine.LoadWhen(audio.Source{Path: info.Playback.Path, URL: info.Playback.URL, Headers: info.Playback.Headers, Format: info.Format}, play); err != nil {
		return fresh, "", playFailure{audioReason(err), err}
	}
	return fresh, a.musicLyrics(t.Source, info.Lyrics), nil
}

func (a *App) loadLyrics(t model.Track, gen int) {
	info, fresh, err := a.resolveMusicInfo(t, "play")
	if err != nil {
		a.sourceError(t, "歌曲信息恢复失败", err)
		a.showCachedLyrics(t, gen)
		a.Alert(infoFailed)
		return
	}
	s := a.musicLyrics(t.Source, info.Lyrics)
	lines := lyrics.Parse(s, a.Cfg.Bool("lrc.multiLang"))
	a.mu.Lock()
	if gen == a.loadGen && a.index >= 0 && a.index < len(a.queue) && a.queue[a.index].Key() == t.Key() {
		a.queue[a.index] = fresh
		a.lyricsRaw, a.lyricLns = s, lines
	}
	a.mu.Unlock()
	a.redraw()
}

func (a *App) TogglePlay() {
	a.mu.Lock()
	loading := a.loading != 0 && a.loading == a.loadGen
	if loading {
		a.wantPlay = !a.wantPlay
	}
	a.mu.Unlock()
	if loading {
		a.redraw()
		return
	}
	if !a.Engine.Loaded() {
		a.mu.Lock()
		i, n := a.activeIndexLocked(), len(a.queue)
		a.mu.Unlock()
		if n > 0 {
			a.playIndexAt(max(i, 0), true, a.State.Float("position"))
			a.State.Set("position", 0)
		}
		return
	}
	if a.Engine.Playing() {
		a.checkManual()
	}
	a.Engine.SetPlaying(!a.Engine.Playing())
	a.redraw()
}

func (a *App) SetPlaying(p bool) {
	if a.Engine.Loaded() {
		a.Engine.SetPlaying(p)
		a.redraw()
	}
}

// Next 切到下一首。auto 为 true 表示由播放结束触发。
func (a *App) Next(auto bool) {
	if !auto {
		a.checkManual()
	}
	a.mu.Lock()
	n := len(a.queue)
	if n == 0 {
		a.mu.Unlock()
		return
	}
	i := a.activeIndexLocked()
	switch {
	case auto && a.mode == ModeRepeat:
		i = max(i, 0)
	default:
		i = (i + 1) % n
	}
	i = a.normalIndexLocked(i)
	a.mu.Unlock()
	a.startTrackAt(i, true, 0, true)
}

// Prev 切到上一首；开启「快速重播」时，在曲目即将结束时会回到当前曲目开头。
func (a *App) Prev() {
	a.checkManual()
	pos, dur := a.Engine.Position()
	if a.Cfg.Bool("ctl.quickReplay") && dur > 0 && dur-pos < 30 {
		a.Engine.Seek(0)
		a.redraw()
		return
	}
	a.mu.Lock()
	n := len(a.queue)
	if n == 0 {
		a.mu.Unlock()
		return
	}
	i := (a.activeIndexLocked() - 1 + n) % n
	i = a.normalIndexLocked(i)
	a.mu.Unlock()
	a.startTrackAt(i, true, 0, true)
}

func (a *App) Seek(sec float64) {
	a.mu.Lock()
	switching := a.loading != 0 && a.loading == a.loadGen && a.switching == a.loadGen
	a.mu.Unlock()
	if switching {
		// The engine still holds the previous song; the one shown is not seekable yet.
		return
	}
	// A restored track is shown paused without its audio loaded; the position it
	// will start from moves instead, and playback stays paused.
	if !a.Engine.Loaded() && a.HasCurrent() {
		a.State.Set("position", max(0, sec))
	}
	a.Engine.Seek(sec)
	a.redraw()
}

func (a *App) SetVolume(v float64) {
	a.Engine.SetVolume(v)
	a.State.Set("volume", v)
	a.redraw()
}

func (a *App) Mode() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *App) CycleMode() int {
	a.mu.Lock()
	a.mode = (a.mode + 1) % 3
	m := a.mode
	a.mu.Unlock()
	a.State.Set("mode", m)
	a.redraw()
	return m
}

func (a *App) onEnded() {
	a.mu.Lock()
	wait := a.sleep.WaitTrackEnd
	a.mu.Unlock()
	if wait {
		a.Engine.SetPlaying(false)
		a.ExitSleep()
		a.execSleepOperation()
		return
	}
	a.Next(true)
}

func (a *App) saveQueue() {
	a.mu.Lock()
	q, i, order := append([]model.Track(nil), a.queue...), a.index, append([]int(nil), a.shuffle...)
	a.mu.Unlock()
	a.State.Set("queue", persistentTracks(q))
	a.State.Set("index", i)
	a.State.Set("shuffleOrder", order)
}

// RestoreQueue 恢复上次的播放队列（不自动播放）。
func (a *App) RestoreQueue() {
	var saved []model.Track
	a.State.Unmarshal("queue", &saved)
	// Queues saved by older versions may hold a song more than once.
	q, index := uniqueTracks(saved, a.State.Int("index"))
	a.mu.Lock()
	a.queue = q
	var order []int
	a.State.Unmarshal("shuffleOrder", &order)
	if !validShuffle(order, len(q)) {
		order = newShuffle(len(q))
	}
	a.shuffle = order
	a.index = -1
	if len(q) > 0 {
		a.index = min(max(index, 0), len(q)-1)
	}
	a.loadGen++
	gen, index := a.loadGen, a.index
	a.wantPlay = false
	if index >= 0 && a.Plugins != nil {
		a.loading = gen
	}
	a.mu.Unlock()
	if index >= 0 && a.Plugins != nil {
		go a.restoreCurrent(q[index], gen)
	}
}

// SavePosition 在退出前保存播放位置。
func (a *App) SavePosition() {
	if a.Engine.Loaded() {
		pos, _ := a.Engine.Position()
		a.State.Set("position", pos)
	}
	a.saveQueue()
	a.State.Flush()
	a.Cfg.Flush()
}

// ---------- 睡眠定时 ----------

func (a *App) Sleep() SleepState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sleep
}

// SetSleep 开启睡眠定时（分钟）。
func (a *App) SetSleep(minutes float64) {
	a.mu.Lock()
	a.sleep = SleepState{Until: time.Now().Add(time.Duration(minutes * float64(time.Minute)))}
	a.mu.Unlock()
	a.Toast("已开启睡眠定时")
	a.redraw()
}

// ExitSleep 退出睡眠定时。
func (a *App) ExitSleep() {
	a.mu.Lock()
	a.sleep.Until, a.sleep.WaitTrackEnd = time.Time{}, false
	a.mu.Unlock()
	a.redraw()
}

// SleepActive 报告睡眠定时是否开启（含延长至曲目结尾阶段）。
func (s SleepState) Active() bool { return !s.Until.IsZero() || s.WaitTrackEnd }

// checkManual 在延长阶段用户手动暂停 / 切歌时退出睡眠定时（与旧版行为一致）。
func (a *App) checkManual() {
	a.mu.Lock()
	wait := a.sleep.WaitTrackEnd
	a.mu.Unlock()
	if wait {
		a.ExitSleep()
		a.Toast("已退出睡眠定时")
	}
}

// CancelPending 取消即将执行的系统操作。
func (a *App) CancelPending() {
	a.mu.Lock()
	a.sleep.Pending = ""
	a.mu.Unlock()
	a.Toast("已取消定时操作")
	a.redraw()
}

func (a *App) ticker() {
	t := time.NewTicker(time.Second)
	for range t.C {
		a.prefetchNext()
		a.mu.Lock()
		s := a.sleep
		a.mu.Unlock()
		now := time.Now()
		if !s.Until.IsZero() && now.After(s.Until) {
			if a.Cfg.Bool("sleep.extendToEnd") && a.Engine.Playing() {
				a.mu.Lock()
				a.sleep.Until, a.sleep.WaitTrackEnd = time.Time{}, true
				a.mu.Unlock()
			} else {
				a.Engine.SetPlaying(false)
				a.ExitSleep()
				a.execSleepOperation()
			}
			a.redraw()
		}
		if s.Pending != "" && now.After(s.PendingAt) {
			a.mu.Lock()
			if a.sleep.Pending != s.Pending || a.sleep.PendingAt != s.PendingAt {
				a.mu.Unlock()
				continue
			}
			a.sleep.Pending = ""
			a.mu.Unlock()
			a.doSystemAction(s.Pending)
		}
		if s.Active() || s.Pending != "" {
			a.redraw()
		}
	}
}

func (a *App) execSleepOperation() {
	act := a.Cfg.String("sleep.action")
	if act != "shutdown" && act != "sleep" {
		a.Toast("睡眠定时已结束")
		return
	}
	a.mu.Lock()
	a.sleep.Pending = act
	a.sleep.PendingAt = time.Now().Add(60 * time.Second)
	a.mu.Unlock()
	a.redraw()
}

// Stop 停止播放并清除当前曲目。
func (a *App) Stop() {
	a.Engine.Unload()
	a.mu.Lock()
	a.index = -1
	a.lyricLns, a.lyricsRaw = nil, ""
	a.loadGen++
	a.mu.Unlock()
	a.saveQueue()
	a.redraw()
}

// SetSpeed 设置播放倍速。
func (a *App) SetSpeed(s float64) {
	a.Engine.SetSpeed(s)
	a.redraw()
}

// ---------- 工具 ----------

func FormatTime(sec float64) string {
	if sec < 0 || sec != sec {
		sec = 0
	}
	s := int(sec + 0.5)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return strconv.Itoa(s/60) + ":" + fmt.Sprintf("%02d", s%60)
}

// TrackActions 返回插件为选中曲目提供的额外右键菜单项（所有曲目须来自同一插件）。
func (a *App) TrackActions(ts []model.Track) []model.Action {
	items, _ := a.SongActions(ts, "list", "")
	return items
}

func (a *App) RunTrackAction(ts []model.Track, key string) {
	go func() {
		if err := a.ExecuteSongAction(ts, key, "list"); err != nil {
			a.fail("操作失败", err)
		}
	}()
}

// PlayNextMany 将多首曲目插入到当前曲目之后；已在队列中的歌曲改为移动到该位置。
func (a *App) PlayNextMany(ts []model.Track) {
	if a.playNextMany(ts) {
		a.Toast("已添加到下一首播放")
	}
}

// playNextMany inserts without a notice; it reports whether songs were placed after the current one.
func (a *App) playNextMany(ts []model.Track) bool {
	ts, _ = uniqueTracks(ts, 0)
	if len(ts) == 0 {
		return false
	}
	a.mu.Lock()
	if len(a.queue) == 0 || a.index < 0 {
		a.mu.Unlock()
		a.PlayQueue(ts, 0)
		return false
	}
	current := a.queue[a.index].Key()
	moved := map[string]bool{}
	kept := ts[:0:0]
	for _, t := range ts {
		if t.Key() != current {
			moved[t.Key()] = true
			kept = append(kept, t)
		}
	}
	ts = kept
	if len(ts) == 0 {
		a.mu.Unlock()
		return false
	}
	for i := len(a.queue) - 1; i >= 0; i-- {
		if moved[a.queue[i].Key()] {
			a.removeAtLocked(i)
		}
	}
	at := a.index + 1
	next := a.shuffleIndexLocked(a.index) + 1
	for i, value := range a.shuffle {
		if value >= at {
			a.shuffle[i] += len(ts)
		}
	}
	order := append([]int{}, a.shuffle[:next]...)
	for i := range ts {
		order = append(order, at+i)
	}
	a.shuffle = append(order, a.shuffle[next:]...)
	q := append([]model.Track{}, a.queue[:at]...)
	q = append(q, ts...)
	a.queue = append(q, a.queue[at:]...)
	a.mu.Unlock()
	a.saveQueue()
	a.redraw()
	return true
}

// HasCurrent 报告是否有当前曲目（决定是否显示底部播放条）。
func (a *App) HasCurrent() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.index >= 0 && a.index < len(a.queue)
}

// showCachedLyrics keeps the lyrics text of the index on screen when the song's information cannot be requested.
func (a *App) showCachedLyrics(t model.Track, gen int) {
	raw := a.cachedLyrics(t)
	if raw == "" {
		return
	}
	lines := lyrics.Parse(raw, a.Cfg.Bool("lrc.multiLang"))
	a.mu.Lock()
	if gen == a.loadGen && a.index >= 0 && a.index < len(a.queue) && a.queue[a.index].Key() == t.Key() {
		a.lyricsRaw, a.lyricLns = raw, lines
	}
	a.mu.Unlock()
	a.redraw()
}
