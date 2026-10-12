// Package desklyric 在 Linux 上：桌面歌词窗口由 CEF 宿主创建和绘制（它有 GTK / Cairo / Pango），
// 这里只保存状态并把它告诉宿主，宿主把拖动和控件点击报告回来。
//
// 命令（一行的各字段，经 Attach 给出的函数发送）：
//
//	lyric opts <颜色 rrggbb> <描边 0|1> <描边颜色> <字号> <宽度> <居中 0|1> <显示翻译 0|1> <翻译比例> <紧凑 0|1> <字体族>
//	lyric pos <x> <y>
//	lyric show <0|1>
//	lyric play <播放中 0|1> <播放模式>
//	lyric line <主歌词> <翻译> <位置秒> <速率> {<开始> <结束> <文字>}...
//
// 宿主报告 "moved <x> <y>" 与 "control <动作>"，交给 HostEvent。
package desklyric

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"sync"
	"time"
)

type Window struct {
	mu      sync.Mutex
	send    func(fields ...string)
	opts    Options
	family  string
	visible bool
	playing bool
	mode    int
	x, y    int
	posSet  bool

	main, sub string
	words     []Word
	pos, rate float64
	posAt     time.Time

	// OnMoved 在拖动结束后调用（屏幕像素坐标）。
	OnMoved   func(x, y int)
	OnControl func(action string)
}

// New 创建桌面歌词（初始隐藏）。Linux 上歌词由宿主按字体族绘制：textTTF 是字体族的名称而不是字体文件，
// iconTTF 不使用（宿主自己加载图标字体）。
func New(opts Options, textTTF, iconTTF []byte) *Window {
	return &Window{opts: opts, family: string(textTTF)}
}

// Attach 接上发往宿主的通道，并把已有的状态全部告诉它。宿主重启后可再次调用。
func (w *Window) Attach(send func(fields ...string)) {
	w.mu.Lock()
	w.send = send
	w.sendOptsLocked()
	if w.posSet {
		w.sendLocked("lyric", "pos", strconv.Itoa(w.x), strconv.Itoa(w.y))
	}
	w.sendLocked("lyric", "play", flag(w.playing), strconv.Itoa(w.mode))
	w.sendLineLocked()
	w.sendLocked("lyric", "show", flag(w.visible))
	w.mu.Unlock()
}

func flag(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

func hex(c color.NRGBA) string { return fmt.Sprintf("%02x%02x%02x", c.R, c.G, c.B) }

func number(value float64) string { return strconv.FormatFloat(value, 'f', 3, 64) }

func (w *Window) sendLocked(fields ...string) {
	if w.send != nil {
		w.send(fields...)
	}
}

func (w *Window) sendOptsLocked() {
	o := w.opts
	w.sendLocked("lyric", "opts", hex(o.Color), flag(o.Stroke), hex(o.StrokeColor), number(o.Size), number(o.Width), flag(o.Center), flag(o.ShowTrans), number(o.TransScale), flag(o.Compact), w.family)
}

func (w *Window) sendLineLocked() {
	fields := []string{"lyric", "line", w.main, w.sub, number(w.pos), number(w.rate)}
	for _, word := range w.words {
		fields = append(fields, number(word.Start), number(word.End), word.Text)
	}
	w.sendLocked(fields...)
}

// SetTextFont 更换歌词字体：data 是字体族的名称（见 New）。
func (w *Window) SetTextFont(data []byte) {
	w.mu.Lock()
	if family := string(data); family != w.family {
		w.family = family
		w.sendOptsLocked()
	}
	w.mu.Unlock()
}

func (w *Window) SetOptions(o Options) {
	w.mu.Lock()
	if o != w.opts {
		w.opts = o
		w.sendOptsLocked()
	}
	w.mu.Unlock()
}

// SetPosition 设置窗口左上角（屏幕像素坐标）。
func (w *Window) SetPosition(x, y int) {
	w.mu.Lock()
	w.x, w.y, w.posSet = x, y, true
	w.sendLocked("lyric", "pos", strconv.Itoa(x), strconv.Itoa(y))
	w.mu.Unlock()
}

// Nudge 把窗口移动若干像素，并像拖动结束一样报告新位置。
func (w *Window) Nudge(dx, dy int) {
	w.mu.Lock()
	if !w.posSet {
		w.mu.Unlock()
		return
	}
	w.x, w.y = w.x+dx, w.y+dy
	x, y, moved := w.x, w.y, w.OnMoved
	w.sendLocked("lyric", "pos", strconv.Itoa(x), strconv.Itoa(y))
	w.mu.Unlock()
	if moved != nil {
		moved(x, y)
	}
}

func (w *Window) SetText(main, sub string) { w.SetLine(main, sub, nil, 0, 0) }

// SetLine 设置显示的这一行。words 非空时逐字显示：position 是此刻的播放位置，之后每秒前进 rate 秒，
// 宿主据此自己推进，这里只在内容变了、或宿主的推算与实际位置差得多时才再发一次。
func (w *Window) SetLine(main, sub string, words []Word, position, rate float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	same := main == w.main && sub == w.sub && len(words) == len(w.words)
	for i := 0; same && i < len(words); i++ {
		same = words[i] == w.words[i]
	}
	expected := w.pos + time.Since(w.posAt).Seconds()*w.rate
	if same && (len(words) == 0 || rate == w.rate && math.Abs(position-expected) < 0.15) {
		return
	}
	w.main, w.sub, w.words = main, sub, append([]Word(nil), words...)
	w.pos, w.rate, w.posAt = position, rate, time.Now()
	w.sendLineLocked()
}

// Dump 在 Windows 上把窗口画面写成图片；宿主的窗口在这里读不到。
func (w *Window) Dump(path string) {}

func (w *Window) SetPlayback(playing bool, mode int) {
	w.mu.Lock()
	if playing != w.playing || mode != w.mode {
		w.playing, w.mode = playing, mode
		w.sendLocked("lyric", "play", flag(playing), strconv.Itoa(mode))
	}
	w.mu.Unlock()
}

// Handle 在 Windows 上是窗口句柄；Linux 上没有可用的。
func (w *Window) Handle() uintptr { return 0 }

func (w *Window) Show(show bool) {
	w.mu.Lock()
	if show != w.visible {
		w.visible = show
		w.sendLocked("lyric", "show", flag(show))
	}
	w.mu.Unlock()
}

func (w *Window) Visible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible
}

// HostEvent 接收宿主对桌面歌词窗口的报告："moved <x> <y>"、"control <动作>"。
func (w *Window) HostEvent(fields []string) {
	if len(fields) == 3 && fields[0] == "moved" {
		x, errX := strconv.Atoi(fields[1])
		y, errY := strconv.Atoi(fields[2])
		if errX != nil || errY != nil {
			return
		}
		w.mu.Lock()
		w.x, w.y, w.posSet = x, y, true
		moved := w.OnMoved
		w.mu.Unlock()
		if moved != nil {
			moved(x, y)
		}
	}
	if len(fields) == 2 && fields[0] == "control" {
		w.mu.Lock()
		control := w.OnControl
		w.mu.Unlock()
		if control != nil {
			control(fields[1])
		}
	}
}
