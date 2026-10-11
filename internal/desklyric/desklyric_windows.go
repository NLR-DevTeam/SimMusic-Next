// Package desklyric 使用 Win32 分层窗口（UpdateLayeredWindow）实现逐像素透明的桌面歌词。
//
// 外观还原旧版：单行居中、加粗、可选描边与翻译；鼠标悬停时显示拖动图标。
// 所有窗口操作都通过 PostMessage / ShowWindowAsync 异步完成，避免跨线程同步调用造成死锁。
package desklyric

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
	"golang.org/x/sys/windows"
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	gdi32                     = windows.NewLazySystemDLL("gdi32.dll")
	kernel                    = windows.NewLazySystemDLL("kernel32.dll")
	pRegisterClassEx          = user32.NewProc("RegisterClassExW")
	pCreateWindowEx           = user32.NewProc("CreateWindowExW")
	pDefWindowProc            = user32.NewProc("DefWindowProcW")
	pGetMessage               = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessage          = user32.NewProc("DispatchMessageW")
	pPostMessage              = user32.NewProc("PostMessageW")
	pShowWindowAsync          = user32.NewProc("ShowWindowAsync")
	pUpdateLayeredWindow      = user32.NewProc("UpdateLayeredWindow")
	pGetDC                    = user32.NewProc("GetDC")
	pReleaseDC                = user32.NewProc("ReleaseDC")
	pSetTimer                 = user32.NewProc("SetTimer")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pSystemParametersInfo     = user32.NewProc("SystemParametersInfoW")
	pGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	pSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pGetWindow                = user32.NewProc("GetWindow")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pGetWindowLongPtr         = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtr         = user32.NewProc("SetWindowLongPtrW")
	pSetCapture               = user32.NewProc("SetCapture")
	pReleaseCapture           = user32.NewProc("ReleaseCapture")
	pDwmGetWindowAttribute    = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmGetWindowAttribute")
	pCreateCompatibleDC       = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection         = gdi32.NewProc("CreateDIBSection")
	pSelectObject             = gdi32.NewProc("SelectObject")
	pDeleteObject             = gdi32.NewProc("DeleteObject")
	pDeleteDC                 = gdi32.NewProc("DeleteDC")
	pGetModuleHandle          = kernel.NewProc("GetModuleHandleW")
)

const (
	wmNCHitTest      = 0x0084
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmMouseWheel     = 0x020A
	wmCaptureChanged = 0x0215
	wmTimer          = 0x0113
	wmMoving         = 0x0216
	wmEnterSizeMove  = 0x0231
	wmExitSizeMove   = 0x0232
	wmApp            = 0x8000
	wmRender         = wmApp + 1
	wmApplyOpts      = wmApp + 2
	htCaption        = 2
	htClient         = 1
	htTransparent    = ^uintptr(0)
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type msg struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }
type size struct{ CX, CY int32 }

// Options 控制桌面歌词外观（尺寸单位为 DIP，与旧版 CSS 像素一致）。
type Options struct {
	Color       color.NRGBA
	Stroke      bool
	StrokeColor color.NRGBA
	Size        float64 // 字号
	Width       float64 // 歌词区域宽度
	Center      bool
	ShowTrans   bool
	TransScale  float64 // 翻译字号（相对主歌词）
	HideCapture bool
	Compact     bool
}

type Window struct {
	mu             sync.Mutex
	hwnd           uintptr
	opts           Options
	main           string
	sub            string
	visible        bool
	hover          bool
	dragging       bool
	playing        bool
	mode           int
	hoverControl   string
	pressedControl string
	mouseEnabled   bool
	x, y           int32 // 窗口左上角（物理像素）
	w, h           int32
	posSet         bool
	ready          chan struct{}

	textFont *opentype.Font
	nextFont *opentype.Font // chosen by SetTextFont, adopted by the window's own thread
	iconFont *opentype.Font
	faces    map[[2]float64]font.Face

	// OnMoved 在拖动结束后调用（物理像素坐标）。
	OnMoved   func(x, y int)
	OnControl func(action string)
}

var current *Window

// New 创建桌面歌词窗口（初始隐藏）。textTTF 为歌词字体，iconTTF 为 RemixIcon 字体。
func New(opts Options, textTTF, iconTTF []byte) *Window {
	w := &Window{opts: opts, ready: make(chan struct{}), faces: map[[2]float64]font.Face{}}
	w.textFont = parseTextFont(textTTF)
	if f, err := opentype.Parse(iconTTF); err == nil {
		w.iconFont = f
	}
	current = w
	go w.thread()
	<-w.ready
	return w
}

// parseTextFont reads a font file or the first face of a collection (msyh.ttc and the like).
func parseTextFont(data []byte) *opentype.Font {
	collection, err := opentype.ParseCollection(data)
	if err != nil || collection.NumFonts() == 0 {
		return nil
	}
	f, err := collection.Font(0)
	if err != nil {
		return nil
	}
	return f
}

// SetTextFont 更换歌词字体（线程安全，异步生效）；无法解析的字体保持原样。
func (w *Window) SetTextFont(data []byte) {
	f := parseTextFont(data)
	if f == nil {
		return
	}
	w.mu.Lock()
	w.nextFont = f
	hwnd := w.hwnd
	w.mu.Unlock()
	pPostMessage.Call(hwnd, wmApplyOpts, 0, 0)
}

func (w *Window) face(f *opentype.Font, px float64) font.Face {
	key := [2]float64{px, 0}
	if f == w.iconFont {
		key[1] = 1
	}
	if fc, ok := w.faces[key]; ok {
		return fc
	}
	if f == nil {
		return nil
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil
	}
	w.faces[key] = fc
	return fc
}

func dpiScale() float64 {
	if pGetDpiForSystem.Find() == nil {
		d, _, _ := pGetDpiForSystem.Call()
		if d > 0 {
			return float64(d) / 96
		}
	}
	return 1
}

func workArea() rect {
	var r rect
	pSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&r)), 0) // SPI_GETWORKAREA
	return r
}

func (w *Window) thread() {
	runtime.LockOSThread()
	inst, _, _ := pGetModuleHandle.Call(0)
	cls, _ := windows.UTF16PtrFromString("SimMusicNextDesktopLyrics")
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: windows.Handle(inst), ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	const exStyle = 0x00080000 | 0x00000008 | 0x00000080 | 0x08000000 | 0x20                 // LAYERED | TOPMOST | TOOLWINDOW | NOACTIVATE | TRANSPARENT
	hwnd, _, _ := pCreateWindowEx.Call(exStyle, uintptr(unsafe.Pointer(cls)), 0, 0x80000000, // WS_POPUP
		0, 0, 100, 100, 0, 0, inst, 0)
	w.mu.Lock()
	w.hwnd = hwnd
	w.layoutLocked()
	w.mu.Unlock()
	w.applyAffinity()
	pSetTimer.Call(hwnd, 1, 50, 0)
	pSetTimer.Call(hwnd, 2, 500, 0) // 置顶守护
	close(w.ready)
	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	w := current
	if w == nil {
		r, _, _ := pDefWindowProc.Call(hwnd, umsg, wparam, lparam)
		return r
	}
	switch umsg {
	case wmNCHitTest:
		var r rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		p := image.Pt(int(int16(lparam&0xffff))-int(r.Left), int(int16((lparam>>16)&0xffff))-int(r.Top))
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.hover || w.dragging {
			if p.In(dragBounds(int(w.w), dpiScale())) {
				return htCaption
			}
			if w.controlAtLocked(p) != "" {
				return htClient
			}
		}
		return htTransparent
	case wmMouseMove:
		w.updateHover()
		return 0
	case wmLButtonDown:
		w.mu.Lock()
		w.pressedControl = w.controlAtLocked(image.Pt(int(int16(lparam&0xffff)), int(int16((lparam>>16)&0xffff))))
		pressed := w.pressedControl != ""
		w.mu.Unlock()
		if pressed {
			pSetCapture.Call(hwnd)
			w.render()
		}
		return 0
	case wmLButtonUp:
		w.mu.Lock()
		action := w.pressedControl
		if w.controlAtLocked(image.Pt(int(int16(lparam&0xffff)), int(int16((lparam>>16)&0xffff)))) != action {
			action = ""
		}
		w.pressedControl = ""
		cb := w.OnControl
		w.mu.Unlock()
		pReleaseCapture.Call()
		w.render()
		if action != "" && cb != nil {
			go cb(action)
		}
		return 0
	case wmMouseWheel:
		// Over the move handle or a playback control the wheel adjusts the volume, one step per notch.
		// The point is in screen coordinates, unlike the button messages.
		var r rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		p := image.Pt(int(int16(lparam&0xffff))-int(r.Left), int(int16((lparam>>16)&0xffff))-int(r.Top))
		delta := int16(wparam >> 16)
		w.mu.Lock()
		over := p.In(dragBounds(int(w.w), dpiScale())) || w.controlAtLocked(p) != ""
		cb := w.OnControl
		w.mu.Unlock()
		if over && delta != 0 && cb != nil {
			action := "volumeUp"
			if delta < 0 {
				action = "volumeDown"
			}
			go cb(action)
		}
		return 0
	case wmCaptureChanged:
		w.mu.Lock()
		w.pressedControl = ""
		w.mu.Unlock()
		w.render()
		return 0
	case wmEnterSizeMove:
		w.mu.Lock()
		w.dragging = true
		w.mu.Unlock()
		w.render()
		return 0
	case wmMoving:
		r := (*rect)(unsafe.Pointer(lparam))
		w.mu.Lock()
		center := w.opts.Center
		w.mu.Unlock()
		if center {
			wa := workArea()
			width := r.Right - r.Left
			r.Left = wa.Left + (wa.Right-wa.Left-width)/2
			r.Right = r.Left + width
		}
		// The position follows the drag as it happens and is saved at once. A lyric line that
		// changes mid-drag is drawn at the stored position, so a stale one would snap the window back.
		w.mu.Lock()
		w.x, w.y, w.posSet = r.Left, r.Top, true
		cb := w.OnMoved
		w.mu.Unlock()
		if cb != nil {
			cb(int(r.Left), int(r.Top))
		}
		return 1
	case wmExitSizeMove:
		var r rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
		w.mu.Lock()
		w.x, w.y, w.posSet, w.dragging = r.Left, r.Top, true, false
		cb := w.OnMoved
		w.mu.Unlock()
		w.render()
		if cb != nil {
			go cb(int(r.Left), int(r.Top))
		}
		return 0
	case wmTimer:
		if wparam == 2 {
			w.guardTopmost(hwnd)
			return 0
		}
		w.updateHover()
		return 0
	case wmApplyOpts:
		w.mu.Lock()
		if w.nextFont != nil {
			w.textFont, w.nextFont = w.nextFont, nil
			for key := range w.faces {
				if key[1] == 0 {
					delete(w.faces, key)
				}
			}
		}
		w.mu.Unlock()
		w.applyAffinity()
		w.render()
		return 0
	case wmRender:
		w.render()
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, umsg, wparam, lparam)
	return r
}

var controlNames = []string{"mode", "previous", "toggle", "next", "close"}

func dragBounds(width int, scale float64) image.Rectangle {
	return image.Rect(width/2-int(18*scale), 0, width/2+int(18*scale), int(20*scale))
}

func controlBounds(width, height, index int, scale float64) image.Rectangle {
	size, gap := int(28*scale), int(6*scale)
	left := (width-len(controlNames)*size-(len(controlNames)-1)*gap)/2 + index*(size+gap)
	top := height - int(32*scale)
	return image.Rect(left, top, left+size, top+size)
}

func (w *Window) controlAtLocked(p image.Point) string {
	if w.opts.Compact || !w.hover {
		return ""
	}
	for i, action := range controlNames {
		if p.In(controlBounds(int(w.w), int(w.h), i, dpiScale())) {
			return action
		}
	}
	return ""
}

func (w *Window) updateHover() {
	var p point
	var r rect
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	w.mu.Lock()
	hwnd := w.hwnd
	w.mu.Unlock()
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	local := image.Pt(int(p.X-r.Left), int(p.Y-r.Top))
	w.mu.Lock()
	hover := w.visible && local.In(image.Rect(0, 0, int(w.w), int(w.h)))
	changed := hover != w.hover
	w.hover = hover
	control := w.controlAtLocked(local)
	changed = changed || control != w.hoverControl
	w.hoverControl = control
	enabled := w.dragging || hover && (local.In(dragBounds(int(w.w), dpiScale())) || control != "" || w.pressedControl != "")
	mouseChanged := enabled != w.mouseEnabled
	w.mouseEnabled = enabled
	w.mu.Unlock()
	if mouseChanged {
		ex, _, _ := pGetWindowLongPtr.Call(hwnd, ^uintptr(19))
		if enabled {
			ex &^= 0x20
		} else {
			ex |= 0x20
		}
		pSetWindowLongPtr.Call(hwnd, ^uintptr(19), ex)
	}
	if changed {
		w.render()
	}
}

func (w *Window) applyAffinity() {
	w.mu.Lock()
	hide, hwnd := w.opts.HideCapture, w.hwnd
	w.mu.Unlock()
	var v uintptr
	if hide {
		v = 0x11 // WDA_EXCLUDEFROMCAPTURE
	}
	pSetWindowDisplayAffinity.Call(hwnd, v)
}

// layoutLocked 根据选项计算窗口尺寸与位置。
func (w *Window) layoutLocked() {
	s := dpiScale()
	wa := workArea()
	sw := wa.Right - wa.Left
	fs := w.opts.Size * s
	cx := w.x + w.w/2 // 以中心为基准调整宽度
	w.w = min(int32(w.opts.Width*s), sw)
	h := 20*s + fs*1.35 + 8*s
	if w.opts.ShowTrans {
		h += fs * w.opts.TransScale * 1.35
	}
	if !w.opts.Compact {
		h += 32 * s
	}
	w.h = int32(h)
	if !w.posSet {
		w.x = wa.Left + (sw-w.w)/2
		w.y = wa.Bottom - w.h - int32(100*s)
	} else {
		w.x = cx - w.w/2
	}
	if w.opts.Center {
		w.x = wa.Left + (sw-w.w)/2
	}
	w.y = max(wa.Top, min(w.y, wa.Bottom-w.h))
}

// SetOptions 更新外观设置（线程安全，异步生效）。
func (w *Window) SetOptions(o Options) {
	w.mu.Lock()
	w.opts = o
	w.layoutLocked()
	hwnd := w.hwnd
	w.mu.Unlock()
	pPostMessage.Call(hwnd, wmApplyOpts, 0, 0)
}

// SetPosition 恢复上次保存的位置（窗口左上角，物理像素）。
func (w *Window) SetPosition(x, y int) {
	w.mu.Lock()
	w.x, w.y, w.posSet = int32(x), int32(y), true
	w.layoutLocked()
	w.mu.Unlock()
}

// Nudge follows the legacy W/A/S/D shortcuts and keeps the overlay on screen.
func (w *Window) Nudge(dx, dy int) {
	w.mu.Lock()
	area := workArea()
	if !w.opts.Center {
		w.x = max(area.Left, min(w.x+int32(dx), area.Right-w.w))
	}
	w.y = max(area.Top, min(w.y+int32(dy), area.Bottom-w.h))
	w.posSet = true
	x, y, hwnd, moved := w.x, w.y, w.hwnd, w.OnMoved
	w.mu.Unlock()
	pPostMessage.Call(hwnd, wmRender, 0, 0)
	if moved != nil {
		moved(int(x), int(y))
	}
}

// SetText 设置当前歌词行与翻译。
func (w *Window) SetText(main, sub string) {
	w.mu.Lock()
	if w.main == main && w.sub == sub {
		w.mu.Unlock()
		return
	}
	w.main, w.sub = main, sub
	hwnd, vis := w.hwnd, w.visible
	w.mu.Unlock()
	if vis {
		pPostMessage.Call(hwnd, wmRender, 0, 0)
	}
}

func (w *Window) SetPlayback(playing bool, mode int) {
	w.mu.Lock()
	changed := w.playing != playing || w.mode != mode
	w.playing, w.mode = playing, mode
	hwnd, visible := w.hwnd, w.visible
	w.mu.Unlock()
	if changed && visible {
		pPostMessage.Call(hwnd, wmRender, 0, 0)
	}
}

func (w *Window) Handle() uintptr {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hwnd
}

// Show 显示 / 隐藏窗口（异步，不会阻塞调用方线程）。
func (w *Window) Show(show bool) {
	w.mu.Lock()
	if w.visible == show {
		w.mu.Unlock()
		return
	}
	w.visible = show
	hwnd := w.hwnd
	w.mu.Unlock()
	if show {
		pPostMessage.Call(hwnd, wmRender, 0, 0)
		pShowWindowAsync.Call(hwnd, 4) // SW_SHOWNOACTIVATE
	} else {
		pShowWindowAsync.Call(hwnd, 0)
	}
}

func (w *Window) Visible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible
}

// render 在窗口线程中绘制并提交分层窗口内容。
func (w *Window) render() {
	w.mu.Lock()
	opts, main, sub, hover, dragging := w.opts, w.main, w.sub, w.hover, w.dragging
	playing, mode, hoverControl, pressedControl := w.playing, w.mode, w.hoverControl, w.pressedControl
	W, H, X, Y := w.w, w.h, w.x, w.y
	hwnd := w.hwnd
	w.mu.Unlock()
	if W <= 0 || H <= 0 {
		return
	}
	s := dpiScale()
	img := image.NewRGBA(image.Rect(0, 0, int(W), int(H)))
	// Lyrics only reveal the handle and controls; they remain click-through.
	bg := color.RGBA{0, 0, 0, 1}
	if dragging {
		bg = color.RGBA{0, 0, 0, 26}
	}
	textBottom := int(H)
	if !opts.Compact {
		textBottom -= int(32 * s)
	}
	fillRoundRect(img, image.Rect(0, int(14*s), int(W), textBottom), 10*s, bg)
	if main == "" {
		main = "SimMusic"
	}
	fs := math.Round(opts.Size * s)
	textFace := w.face(w.textFont, fs)
	if textFace == nil {
		w.commit(hwnd, img, X, Y)
		return
	}
	col := color.RGBA{opts.Color.R, opts.Color.G, opts.Color.B, 255}
	stroke := color.RGBA{opts.StrokeColor.R, opts.StrokeColor.G, opts.StrokeColor.B, 255}
	y := 20*s + fs*1.05
	drawLine := func(txt string, face font.Face, lineH float64) {
		dr := &font.Drawer{Face: face}
		adv := dr.MeasureString(txt).Round()
		x := (int(W) - adv) / 2
		if adv > int(W)-int(10*s) {
			txt = ellipsis(face, txt, int(W)-int(10*s))
			adv = (&font.Drawer{Face: face}).MeasureString(txt).Round()
			x = (int(W) - adv) / 2
		}
		if opts.Stroke {
			rad := math.Max(1, s)
			for k := 0; k < 12; k++ {
				a := float64(k) * math.Pi / 6
				drawString(img, face, float64(x)+rad*math.Cos(a), y+rad*math.Sin(a), txt, stroke)
			}
		}
		// 叠加绘制模拟加粗（字体只有常规字重）
		drawString(img, face, float64(x), y, txt, col)
		drawString(img, face, float64(x)+0.6*s, y, txt, col)
		y += lineH
	}
	drawLine(main, textFace, fs*1.3)
	if opts.ShowTrans && sub != "" {
		tf := w.face(w.textFont, math.Round(fs*opts.TransScale))
		y += fs*opts.TransScale*1.05 - fs*1.05 + fs*0.1
		drawLine(sub, tf, 0)
	}
	// 悬停时在上方显示拖动图标（RemixIcon drag-move-line）
	if (hover || dragging) && w.iconFont != nil {
		iface := w.face(w.iconFont, math.Round(16*s))
		g := string(rune(0xEC62))
		adv := (&font.Drawer{Face: iface}).MeasureString(g).Round()
		ix := float64((int(W) - adv) / 2)
		for k := 0; k < 8; k++ {
			a := float64(k) * math.Pi / 4
			drawString(img, iface, ix+1.2*math.Cos(a), 15*s+1.2*math.Sin(a), g, color.RGBA{0, 0, 0, 60})
		}
		drawString(img, iface, ix, 15*s, g, color.RGBA{255, 255, 255, 190})
	}
	if hover && !opts.Compact && w.iconFont != nil {
		iface := w.face(w.iconFont, math.Round(20*s))
		playIcon := rune(0xF508)
		if playing {
			playIcon = 0xF507
		}
		modeIcons := []rune{0xF072, 0xF075, 0xF124}
		icons := []rune{modeIcons[max(0, min(mode, 2))], 0xF140, playIcon, 0xF144, 0xEB99}
		for i, action := range controlNames {
			r := controlBounds(int(W), int(H), i, s)
			fillRoundRect(img, r, 5*s, color.RGBA{0, 0, 0, 130})
			if hoverControl == action {
				alpha := uint8(24)
				if pressedControl == action {
					alpha = 45
				}
				fillRoundRect(img, r, 5*s, color.RGBA{alpha, alpha, alpha, alpha})
			}
			g := string(icons[i])
			bounds, _ := font.BoundString(iface, g)
			x := float64(r.Min.X+r.Max.X)/2 - float64(bounds.Min.X+bounds.Max.X)/128
			y := float64(r.Min.Y+r.Max.Y)/2 - float64(bounds.Min.Y+bounds.Max.Y)/128
			if action == "toggle" && !playing {
				x += 1.75 * s
			}
			drawString(img, iface, x, y, g, color.RGBA{235, 235, 235, 235})
		}
	}
	w.commit(hwnd, img, X, Y)
}

func ellipsis(face font.Face, s string, maxW int) string {
	r := []rune(s)
	for len(r) > 0 {
		r = r[:len(r)-1]
		t := string(r) + "…"
		if (&font.Drawer{Face: face}).MeasureString(t).Round() <= maxW {
			return t
		}
	}
	return ""
}

func drawString(img *image.RGBA, face font.Face, x, y float64, s string, c color.RGBA) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face,
		Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	d.DrawString(s)
}

func fillRoundRect(img *image.RGBA, r image.Rectangle, rad float64, c color.RGBA) {
	if c.A == 0 {
		return
	}
	b := img.Bounds()
	ras := vector.NewRasterizer(b.Dx(), b.Dy())
	x0, y0, x1, y1 := float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)
	k := float32(rad)
	ras.MoveTo(x0+k, y0)
	ras.LineTo(x1-k, y0)
	ras.QuadTo(x1, y0, x1, y0+k)
	ras.LineTo(x1, y1-k)
	ras.QuadTo(x1, y1, x1-k, y1)
	ras.LineTo(x0+k, y1)
	ras.QuadTo(x0, y1, x0, y1-k)
	ras.LineTo(x0, y0+k)
	ras.QuadTo(x0, y0, x0+k, y0)
	ras.ClosePath()
	ras.Draw(img, b, image.NewUniform(c), image.Point{})
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func (w *Window) commit(hwnd uintptr, img *image.RGBA, x, y int32) {
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	screenDC, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screenDC)
	memDC, _, _ := pCreateCompatibleDC.Call(screenDC)
	defer pDeleteDC.Call(memDC)
	bi := bitmapInfoHeader{Width: int32(W), Height: -int32(H), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(memDC, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 {
		return
	}
	defer pDeleteObject.Call(hbm)
	old, _, _ := pSelectObject.Call(memDC, hbm)
	defer pSelectObject.Call(memDC, old)
	dst := unsafe.Slice((*byte)(bits), W*H*4)
	// image.RGBA 已是预乘 alpha，只需交换 R/B
	src := img.Pix
	for i := 0; i < len(dst); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	pos := point{x, y}
	sz := size{int32(W), int32(H)}
	var zero point
	blend := uint32(0) | 0<<8 | 255<<16 | 1<<24 // AC_SRC_OVER, 0, 255, AC_SRC_ALPHA
	pUpdateLayeredWindow.Call(hwnd, screenDC, uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&sz)),
		memDC, uintptr(unsafe.Pointer(&zero)), 0, uintptr(unsafe.Pointer(&blend)), 2)
}

// guardTopmost 每 500ms 检查一次：若窗口失去 TOPMOST 属性，或被其他可见的置顶窗口覆盖，
// 则重新将自己置于最顶层。Windows 的置顶窗口之间没有稳定的先后顺序，仅靠 WS_EX_TOPMOST 经常会被盖住。
func (w *Window) guardTopmost(hwnd uintptr) {
	w.mu.Lock()
	vis, dragging := w.visible, w.dragging
	w.mu.Unlock()
	if !vis || dragging {
		return
	}
	const (
		gwlExStyle  = ^uintptr(19) // -20
		wsExTopmost = 0x8
		gwHwndPrev  = 3
		hwndTopmost = ^uintptr(0)                       // -1
		swpFlags    = 0x0001 | 0x0002 | 0x0010 | 0x0200 // NOSIZE | NOMOVE | NOACTIVATE | NOOWNERZORDER
	)
	ex, _, _ := pGetWindowLongPtr.Call(hwnd, gwlExStyle)
	covered := ex&wsExTopmost == 0
	if !covered {
		var me rect
		pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&me)))
		// 沿 Z 序向上遍历，查找与自身相交的可见置顶窗口
		for h, _, _ := pGetWindow.Call(hwnd, gwHwndPrev); h != 0; h, _, _ = pGetWindow.Call(h, gwHwndPrev) {
			if v, _, _ := pIsWindowVisible.Call(h); v == 0 {
				continue
			}
			if e, _, _ := pGetWindowLongPtr.Call(h, gwlExStyle); e&wsExTopmost == 0 {
				continue
			}
			var cloaked uint32
			if pDwmGetWindowAttribute.Find() == nil {
				pDwmGetWindowAttribute.Call(h, 14, uintptr(unsafe.Pointer(&cloaked)), 4) // DWMWA_CLOAKED
			}
			if cloaked != 0 {
				continue
			}
			var r rect
			pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
			if r.Left < me.Right && r.Right > me.Left && r.Top < me.Bottom && r.Bottom > me.Top {
				covered = true
				break
			}
		}
	}
	if covered {
		pSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpFlags)
	}
}
