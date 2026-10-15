// Package overlay draws the application's context menus and notices as windows of their own above
// the main window: layered windows with per-pixel transparency, so they fade, cast their shadow and
// may reach beyond the main window, whatever it shows beneath them (the host page, an extension's
// panel, the play page). The look follows the HTML versions they replace; sizes are CSS pixels scaled
// by the window's DPI. Text is drawn by GDI, shapes are computed here.
//
// Everything happens on one thread of its own; the rest of the program only posts work to it.
package overlay

import (
	"math"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pGetMessage          = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pPostMessage         = user32.NewProc("PostMessageW")
	pSetTimer            = user32.NewProc("SetTimer")
	pKillTimer           = user32.NewProc("KillTimer")
	pUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	pShowWindow          = user32.NewProc("ShowWindow")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	pGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	pGetClientRect       = user32.NewProc("GetClientRect")
	pClientToScreen      = user32.NewProc("ClientToScreen")
	pIsWindowVisible     = user32.NewProc("IsWindowVisible")
	pIsIconic            = user32.NewProc("IsIconic")
	pGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	pMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfo      = user32.NewProc("GetMonitorInfoW")
	pDrawText            = user32.NewProc("DrawTextW")
	pGetDC               = user32.NewProc("GetDC")
	pReleaseDC           = user32.NewProc("ReleaseDC")

	pCreateCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection     = gdi32.NewProc("CreateDIBSection")
	pSelectObject         = gdi32.NewProc("SelectObject")
	pDeleteObject         = gdi32.NewProc("DeleteObject")
	pDeleteDC             = gdi32.NewProc("DeleteDC")
	pCreateFont           = gdi32.NewProc("CreateFontW")
	pSetTextColor         = gdi32.NewProc("SetTextColor")
	pSetBkMode            = gdi32.NewProc("SetBkMode")
	pGetTextExtentPoint32 = gdi32.NewProc("GetTextExtentPoint32W")
	pAddFontMemResourceEx = gdi32.NewProc("AddFontMemResourceEx")
	pGdiFlush             = gdi32.NewProc("GdiFlush")
	pGetModuleHandle      = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

const (
	wmTimer         = 0x0113
	wmLButtonUp     = 0x0202
	wmMouseWheel    = 0x020A
	wmMouseActivate = 0x0021
	wmApp           = 0x8000
	dtFlags         = 0x0020 | 0x0004 | 0x0800 | 0x8000 // SINGLELINE | VCENTER | NOPREFIX | END_ELLIPSIS
)

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }

// Overlay is the set of menu and notice windows.
type Overlay struct {
	host   Host
	jobs   chan func()
	hwnd   uintptr // receives posted work and the timer
	class  *uint16
	inst   uintptr
	ticked bool

	scale    float64
	family   string
	fontSize map[int]uintptr
	iconSize map[int]uintptr
	screenDC uintptr

	menuID     int
	choose     func(path []int) // set for a menu whose rows act here, not in the host page
	screen     bool             // what is being built belongs to the screen, not to the main window: a menu, as against a notice
	levels     []*level
	dying      []*level
	opened     time.Time
	foreground uintptr
	pressed    bool
	leave      time.Time // when the pointer left the open submenu and its row
	toasts     []*toast
}

var current *Overlay

// Start creates the overlay's thread.
func Start(host Host) *Overlay {
	o := &Overlay{host: host, jobs: make(chan func(), 64), fontSize: map[int]uintptr{}, iconSize: map[int]uintptr{}}
	current = o
	ready := make(chan struct{})
	go o.thread(ready)
	<-ready
	return o
}

func (o *Overlay) post(job func()) {
	select {
	case o.jobs <- job:
		pPostMessage.Call(o.hwnd, wmApp, 0, 0)
	default:
	}
}

// ShowMenu opens a menu of the host page where the pointer is, above every other window and whether or not
// the main window is shown: a menu may be asked for from outside it (a taskbar widget of an extension).
func (o *Overlay) ShowMenu(id int, items []Item) {
	o.post(func() { o.showMenu(id, items) })
}

// ShowMenuAtPointer opens such a menu whose rows act here instead of in the host page:
// the menu of the tray icon. choose is called with the path of the row chosen.
func (o *Overlay) ShowMenuAtPointer(items []Item, choose func(path []int)) {
	o.post(func() { o.showScreenMenu(items, choose) })
}

// SetChildren supplies the rows of a lazy row that was opened.
func (o *Overlay) SetChildren(id int, path []int, items []Item) {
	o.post(func() { o.setChildren(id, path, items) })
}

// Toast shows a short notice above the bottom of the main window.
func (o *Overlay) Toast(text string) { o.post(func() { o.showToast(text) }) }

func (o *Overlay) thread(ready chan struct{}) {
	runtime.LockOSThread()
	// Its windows are placed and drawn in real pixels, whatever the rest of the process is told about the screen.
	user32.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(3)) // PER_MONITOR_AWARE_V2
	o.inst, _, _ = pGetModuleHandle.Call(0)
	o.class, _ = windows.UTF16PtrFromString("SimMusicNextOverlay")
	type wndClassEx struct {
		Size, Style                        uint32
		WndProc                            uintptr
		ClsExtra, WndExtra                 int32
		Instance, Icon, Cursor, Background windows.Handle
		MenuName, ClassName                *uint16
		IconSm                             windows.Handle
	}
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512) // IDC_ARROW
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: windows.Handle(o.inst), ClassName: o.class, Cursor: windows.Handle(cursor)}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	o.hwnd, _, _ = pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(o.class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, o.inst, 0) // HWND_MESSAGE
	if len(o.host.IconFont) > 0 {
		var count uint32
		pAddFontMemResourceEx.Call(uintptr(unsafe.Pointer(&o.host.IconFont[0])), uintptr(len(o.host.IconFont)), 0, uintptr(unsafe.Pointer(&count)))
	}
	o.screenDC, _, _ = pGetDC.Call(0)
	close(ready)
	var m struct {
		Hwnd    uintptr
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      point
	}
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	o := current
	switch message {
	case wmApp:
		for {
			select {
			case job := <-o.jobs:
				job()
				continue
			default:
			}
			break
		}
		return 0
	case wmTimer:
		o.tick()
		return 0
	case wmMouseActivate:
		return 3 // MA_NOACTIVATE
	case wmLButtonUp:
		o.click(hwnd, int(int16(lparam&0xffff)), int(int16(lparam>>16&0xffff)))
		return 0
	case wmMouseWheel:
		o.wheel(int(int16(wparam >> 16)))
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, message, wparam, lparam)
	return r
}

// ---------- surfaces ----------

// surface is a window and the premultiplied BGRA picture it shows.
type surface struct {
	hwnd          uintptr
	dc, bitmap    uintptr
	previous      uintptr
	pix           []byte
	w, h          int
	x, y          int // where it rests, in screen pixels
	dx, dy        int // how far from there it is when fully transparent
	alpha         float64
	from, to      float64
	start         time.Time
	duration      time.Duration
	closing, gone bool
}

func (o *Overlay) newSurface(w, h int, clickThrough bool) *surface {
	s := &surface{w: w, h: h}
	type bitmapInfo struct {
		Size                                uint32
		Width, Height                       int32
		Planes, BitCount                    uint16
		Compression, SizeImage              uint32
		XPels, YPels, ClrUsed, ClrImportant int32
	}
	info := bitmapInfo{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	info.Size = uint32(unsafe.Sizeof(info))
	var bits unsafe.Pointer
	s.dc, _, _ = pCreateCompatibleDC.Call(o.screenDC)
	s.bitmap, _, _ = pCreateDIBSection.Call(o.screenDC, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if s.bitmap == 0 || bits == nil {
		pDeleteDC.Call(s.dc)
		return nil
	}
	s.previous, _, _ = pSelectObject.Call(s.dc, s.bitmap)
	s.pix = unsafe.Slice((*byte)(bits), w*h*4)
	ex := uintptr(0x00080000 | 0x00000080 | 0x08000000) // LAYERED | TOOLWINDOW | NOACTIVATE
	if clickThrough {
		ex |= 0x20 // TRANSPARENT
	}
	owner := o.host.Owner()
	if o.screen {
		// Not owned by the main window, which may be hidden or minimized, and above the taskbar.
		ex, owner = ex|0x8, 0 // TOPMOST
	}
	s.hwnd, _, _ = pCreateWindowEx.Call(ex, uintptr(unsafe.Pointer(o.class)), 0, 0x80000000, 0, 0, uintptr(w), uintptr(h), owner, 0, o.inst, 0)
	return s
}

func (s *surface) destroy() {
	pDestroyWindow.Call(s.hwnd)
	pSelectObject.Call(s.dc, s.previous)
	pDeleteObject.Call(s.bitmap)
	pDeleteDC.Call(s.dc)
	s.gone = true
}

// fade starts moving the surface's opacity; it also moves between its offset and resting places.
func (s *surface) fade(to float64, duration time.Duration) {
	s.from, s.to, s.start, s.duration = s.alpha, to, time.Now(), duration
}

// ease is the timing of a CSS "ease" transition: cubic-bezier(.25, .1, .25, 1).
func ease(t float64) float64 {
	low, high, s := 0.0, 1.0, t
	for i := 0; i < 20; i++ {
		s = (low + high) / 2
		if 3*(1-s)*(1-s)*s*.25+3*(1-s)*s*s*.25+s*s*s < t {
			low = s
		} else {
			high = s
		}
	}
	return 3*(1-s)*(1-s)*s*.1 + 3*(1-s)*s*s + s*s*s
}

// step advances the fade and shows the surface; false once a closing surface has faded out.
func (s *surface) step() bool {
	if s.duration > 0 {
		t := float64(time.Since(s.start)) / float64(s.duration)
		if t >= 1 {
			s.alpha, s.duration = s.to, 0
		} else {
			s.alpha = s.from + (s.to-s.from)*ease(t)
		}
	}
	if s.closing && s.duration == 0 {
		return false
	}
	s.present()
	return true
}

func (s *surface) present() {
	pGdiFlush.Call()
	place := point{int32(s.x + int(math.Round(float64(s.dx)*(1-s.alpha)))), int32(s.y + int(math.Round(float64(s.dy)*(1-s.alpha))))}
	size := point{int32(s.w), int32(s.h)}
	var origin point
	blend := [4]byte{0, 0, byte(math.Round(255 * max(0, min(1, s.alpha)))), 1} // AC_SRC_OVER, constant alpha, AC_SRC_ALPHA
	pUpdateLayeredWindow.Call(s.hwnd, 0, uintptr(unsafe.Pointer(&place)), uintptr(unsafe.Pointer(&size)), s.dc, uintptr(unsafe.Pointer(&origin)), 0, uintptr(unsafe.Pointer(&blend)), 2)
	pShowWindow.Call(s.hwnd, 4) // SW_SHOWNOACTIVATE
}

func (s *surface) clear() {
	for i := range s.pix {
		s.pix[i] = 0
	}
}

// blend puts a colour of the given opacity over one pixel.
func (s *surface) blend(x, y int, r, g, b byte, a float64) {
	if a <= 0 || x < 0 || y < 0 || x >= s.w || y >= s.h {
		return
	}
	a = min(1, a)
	p := s.pix[(y*s.w+x)*4:]
	p[0] = byte(float64(b)*a + float64(p[0])*(1-a))
	p[1] = byte(float64(g)*a + float64(p[1])*(1-a))
	p[2] = byte(float64(r)*a + float64(p[2])*(1-a))
	p[3] = byte(255*a + float64(p[3])*(1-a))
}

// distance is how far a point lies outside a rounded rectangle (negative inside).
func distance(px, py, x, y, w, h, radius float64) float64 {
	qx := math.Abs(px-(x+w/2)) - (w/2 - radius)
	qy := math.Abs(py-(y+h/2)) - (h/2 - radius)
	return math.Hypot(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - radius
}

func (s *surface) roundRect(x, y, w, h, radius float64, r, g, b byte, a float64) {
	for py := int(y) - 1; py <= int(y+h)+1; py++ {
		for px := int(x) - 1; px <= int(x+w)+1; px++ {
			s.blend(px, py, r, g, b, a*max(0, min(1, .5-distance(float64(px)+.5, float64(py)+.5, x, y, w, h, radius))))
		}
	}
}

// shadow draws what CSS box-shadow draws for a rounded box: black of the given opacity, moved down by
// offset and blurred. The blur radius of CSS is twice the deviation of the Gaussian it blurs with, so the
// shadow is at half strength on the moved outline and gone about one radius outside it. As in CSS, nothing
// of it is drawn under the box itself, which may be translucent.
func (s *surface) shadow(x, y, w, h, radius, offset, blur, a float64) {
	sigma := blur / 2
	for py := 0; py < s.h; py++ {
		for px := 0; px < s.w; px++ {
			cx, cy := float64(px)+.5, float64(py)+.5
			outside := 1 - max(0, min(1, .5-distance(cx, cy, x, y, w, h, radius)))
			if outside <= 0 {
				continue
			}
			s.blend(px, py, 0, 0, 0, a*outside*.5*math.Erfc(distance(cx, cy, x, y+offset, w, h, radius)/(sigma*math.Sqrt2)))
		}
	}
}

// GDI smooths text along the line only, and fits small sizes to the pixel grid: curves and diagonals come
// out stepped, unlike the text of the pages, which is smoothed in both directions. So text is drawn several
// times larger than wanted and scaled down: every pixel is then the share of it the glyph covers.
const oversample = 3

// font returns the font for text of the given pixel size, at the larger size it is drawn in.
func (o *Overlay) font(icon bool, px int) uintptr {
	cache, face := o.fontSize, o.family
	if icon {
		cache, face = o.iconSize, "remixicon"
	}
	if f, ok := cache[px]; ok {
		return f
	}
	name, _ := windows.UTF16PtrFromString(face)
	f, _, _ := pCreateFont.Call(uintptr(-px*oversample), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 4, 0, uintptr(unsafe.Pointer(name))) // DEFAULT_CHARSET, ANTIALIASED_QUALITY
	cache[px] = f
	return f
}

func (o *Overlay) textWidth(font uintptr, text string) int {
	chars := windows.StringToUTF16(text)
	old, _, _ := pSelectObject.Call(o.screenDC, font)
	var size point
	pGetTextExtentPoint32.Call(o.screenDC, uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)-1), uintptr(unsafe.Pointer(&size)))
	pSelectObject.Call(o.screenDC, old)
	return (int(size.X) + oversample - 1) / oversample
}

// text draws one line into a rectangle of the surface. GDI knows nothing of transparency, so the line is
// drawn white on black beside the picture and what it covers is then laid over the picture in colour.
func (o *Overlay) text(s *surface, font uintptr, text string, x, y, w, h int, r, g, b byte, a float64, flags uintptr) {
	if text == "" || w <= 0 || h <= 0 {
		return
	}
	scratch := o.scratch(w*oversample, h*oversample)
	if scratch == nil {
		return
	}
	// The scratch picture is at least as large; only its first w columns of each row are used.
	chars := windows.StringToUTF16(text)
	old, _, _ := pSelectObject.Call(scratch.dc, font)
	pSetBkMode.Call(scratch.dc, 1)
	pSetTextColor.Call(scratch.dc, 0xffffff)
	area := rect{0, 0, int32(w * oversample), int32(h * oversample)}
	pDrawText.Call(scratch.dc, uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)-1), uintptr(unsafe.Pointer(&area)), flags)
	pSelectObject.Call(scratch.dc, old)
	pGdiFlush.Call()
	for row := 0; row < h; row++ {
		for column := 0; column < w; column++ {
			cover := 0
			for dy := 0; dy < oversample; dy++ {
				line := scratch.pix[((row*oversample+dy)*scratch.w+column*oversample)*4:]
				for dx := 0; dx < oversample; dx++ {
					cover += int(line[dx*4+1])
				}
			}
			if cover != 0 {
				s.blend(x+column, y+row, r, g, b, a*float64(cover)/(255*oversample*oversample))
			}
		}
	}
}

var scratchSurface *surface

func (o *Overlay) scratch(w, h int) *surface {
	if s := scratchSurface; s != nil && s.w >= w && s.h >= h {
		// Rows are addressed by the scratch picture's own width, so a smaller request clears a little more.
		for i := 0; i < h; i++ {
			row := s.pix[i*s.w*4 : (i*s.w+w)*4]
			for j := range row {
				row[j] = 0
			}
		}
		return s
	}
	if scratchSurface != nil {
		pSelectObject.Call(scratchSurface.dc, scratchSurface.previous)
		pDeleteObject.Call(scratchSurface.bitmap)
		pDeleteDC.Call(scratchSurface.dc)
	}
	s := &surface{w: max(w, 512), h: max(h, 64)}
	info := struct {
		Size                                uint32
		Width, Height                       int32
		Planes, BitCount                    uint16
		Compression, SizeImage              uint32
		XPels, YPels, ClrUsed, ClrImportant int32
	}{Width: int32(s.w), Height: -int32(s.h), Planes: 1, BitCount: 32}
	info.Size = uint32(unsafe.Sizeof(info))
	var bits unsafe.Pointer
	s.dc, _, _ = pCreateCompatibleDC.Call(o.screenDC)
	s.bitmap, _, _ = pCreateDIBSection.Call(o.screenDC, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if s.bitmap == 0 || bits == nil {
		pDeleteDC.Call(s.dc)
		scratchSurface = nil
		return nil
	}
	s.previous, _, _ = pSelectObject.Call(s.dc, s.bitmap)
	s.pix = unsafe.Slice((*byte)(bits), s.w*s.h*4)
	scratchSurface = s
	return s
}

// ---------- shared ----------

// prepare reads the owner's scale and the interface font; false when there is no window to appear above.
func (o *Overlay) prepare() bool {
	owner := o.host.Owner()
	if owner == 0 {
		return false
	}
	if visible, _, _ := pIsWindowVisible.Call(owner); visible == 0 {
		return false
	}
	if iconic, _, _ := pIsIconic.Call(owner); iconic != 0 {
		return false
	}
	dpi, _, _ := pGetDpiForWindow.Call(owner)
	o.setScale(float64(dpi) / 96)
	return true
}

// prepareAt does the same for something shown at a point of the screen, whatever the main window does.
func (o *Overlay) prepareAt(x, y int) {
	monitor, _, _ := pMonitorFromPoint.Call(uintptr(uint32(x))|uintptr(uint32(y))<<32, 2)
	var dpiX, dpiY uint32
	windows.NewLazySystemDLL("shcore.dll").NewProc("GetDpiForMonitor").Call(monitor, 0, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY)))
	if dpiX == 0 {
		dpiX = 96
	}
	o.setScale(float64(dpiX) / 96)
}

func (o *Overlay) setScale(scale float64) {
	scale = max(1, scale)
	family := o.host.Font()
	if family == "" {
		family = "Microsoft YaHei"
	}
	if scale != o.scale || family != o.family {
		for _, cache := range []map[int]uintptr{o.fontSize, o.iconSize} {
			for size, font := range cache {
				pDeleteObject.Call(font)
				delete(cache, size)
			}
		}
		o.scale, o.family = scale, family
	}
}

func (o *Overlay) px(css float64) int { return int(math.Round(css * o.scale)) }

func (o *Overlay) ownerClient() rect {
	var client rect
	var origin point
	pGetClientRect.Call(o.host.Owner(), uintptr(unsafe.Pointer(&client)))
	pClientToScreen.Call(o.host.Owner(), uintptr(unsafe.Pointer(&origin)))
	return rect{origin.X, origin.Y, origin.X + client.Right, origin.Y + client.Bottom}
}

func workArea(x, y int) rect {
	monitor, _, _ := pMonitorFromPoint.Call(uintptr(uint32(x))|uintptr(uint32(y))<<32, 2)
	info := struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}{}
	info.Size = uint32(unsafe.Sizeof(info))
	pGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info)))
	return info.Work
}

func cursor() (int, int) {
	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

func keyDown(key uintptr) bool {
	state, _, _ := pGetAsyncKeyState.Call(key)
	return state&0x8000 != 0
}

func (o *Overlay) run() {
	if !o.ticked {
		o.ticked = true
		pSetTimer.Call(o.hwnd, 1, 10, 0)
	}
}

func (o *Overlay) tick() {
	o.tickMenu()
	o.tickToasts()
	if len(o.levels) == 0 && len(o.dying) == 0 && len(o.toasts) == 0 {
		o.ticked = false
		pKillTimer.Call(o.hwnd, 1)
	}
}
