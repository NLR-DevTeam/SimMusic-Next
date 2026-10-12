package overlay

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Menus: the main list and the lists opened from its rows, each a window of its own. Measures are those
// of the HTML menu: 4px padding inside a 1px border, rows 31px high with 6px/10px padding, a 16px icon
// column, 14px text, corners of 8px, rows of 5px.
const (
	menuMargin = 40.0 // room around the box for its shadow
	rowHeight  = 31.0
	arrowGlyph = ""
	iconFlags  = 0x0020 | 0x0004 | 0x0800 | 0x0001 | 0x0100 // SINGLELINE | VCENTER | NOPREFIX | CENTER | NOCLIP
)

type level struct {
	*surface
	items          []Item
	path           []int // the row this list was opened from; nil for the main list
	place          func(w, h int) (x, y int, flipped bool)
	offsetX        float64 // where it comes from while fading in, in CSS pixels
	offsetY        float64
	tops, heights  []int
	contentH       int
	viewH, scroll  int
	bx, by, bw, bh int
	hover, open    int
	icons, arrows  bool
	inert          bool // rows that only say something: "reading…", "nothing here"
}

func (l *level) contains(x, y int) bool {
	return x >= l.x+l.bx && x < l.x+l.bx+l.bw && y >= l.y+l.by && y < l.y+l.by+l.bh
}

// rowAt returns the row under a screen point, or -1.
func (l *level) rowAt(o *Overlay, x, y int) int {
	if !l.contains(x, y) {
		return -1
	}
	inner := y - l.y - l.by - o.px(5) + l.scroll
	for i := range l.items {
		if !l.items[i].Separator && inner >= l.tops[i] && inner < l.tops[i]+l.heights[i] {
			return i
		}
	}
	return -1
}

// build measures the list and makes its window; the picture is drawn by render.
func (o *Overlay) build(l *level) bool {
	text := o.font(false, o.px(14))
	widest := 0
	l.icons, l.arrows = false, false
	l.tops, l.heights = make([]int, len(l.items)), make([]int, len(l.items))
	l.contentH = 0
	for i, item := range l.items {
		l.tops[i] = l.contentH
		l.heights[i] = o.px(rowHeight)
		if item.Separator {
			l.heights[i] = o.px(9)
		} else {
			widest = max(widest, o.textWidth(text, item.Label))
			l.icons = l.icons || item.Icon != ""
			l.arrows = l.arrows || item.Lazy || len(item.Children) > 0
		}
		l.contentH += l.heights[i]
	}
	width := widest + o.px(20) + o.px(10) + o.px(2) // the last for what rounding the parts may cost the label
	if l.icons {
		width += o.px(21)
	}
	if l.arrows {
		width += o.px(31)
	}
	l.bw = max(o.px(180), min(o.px(320), width))
	cx, cy := cursor()
	area := workArea(cx, cy)
	l.viewH = min(l.contentH, int(area.Bottom-area.Top)-o.px(18))
	l.bh = l.viewH + o.px(10)
	l.bx, l.by = o.px(menuMargin), o.px(menuMargin)
	if l.surface != nil {
		l.surface.destroy()
	}
	l.surface = o.newSurface(l.bw+2*l.bx, l.bh+2*l.by, false)
	if l.surface == nil {
		return false
	}
	x, y, flipped := l.place(l.bw, l.bh)
	l.x, l.y = x-l.bx, y-l.by
	l.dx, l.dy = o.px(l.offsetX), o.px(l.offsetY)
	if flipped {
		l.dy = -l.dy
	}
	l.scroll = max(0, min(l.scroll, l.contentH-l.viewH))
	o.render(l)
	return true
}

func (o *Overlay) render(l *level) {
	s := l.surface
	s.clear()
	x, y, w, h := float64(l.bx), float64(l.by), float64(l.bw), float64(l.bh)
	s.shadow(x, y, w, h, o.scale*8, o.scale*8, o.scale*24, .14)
	s.roundRect(x, y, w, h, o.scale*8, 255, 255, 255, .95)
	// The hairline border: the outer pixel of the box, #0000000d.
	edge := max(1, float64(o.px(1)))
	for py := l.by - 1; py <= l.by+l.bh; py++ {
		for px := l.bx - 1; px <= l.bx+l.bw; px++ {
			d := distance(float64(px)+.5, float64(py)+.5, x, y, w, h, o.scale*8)
			s.blend(px, py, 0, 0, 0, .05*(max(0, min(1, .5-d))-max(0, min(1, .5-(d+edge)))))
		}
	}
	text, icons, arrows := o.font(false, o.px(14)), o.font(true, o.px(16)), o.font(true, o.px(13))
	inset := o.px(5) // padding and border
	for i, item := range l.items {
		top := l.by + inset + l.tops[i] - l.scroll
		if top < l.by+inset || top+l.heights[i] > l.by+inset+l.viewH {
			continue
		}
		if item.Separator {
			line := max(1, o.px(1))
			for py := top + o.px(4); py < top+o.px(4)+line; py++ {
				for px := l.bx + inset + o.px(6); px < l.bx+l.bw-inset-o.px(6); px++ {
					s.blend(px, py, 0, 0, 0, .05)
				}
			}
			continue
		}
		left, width := l.bx+inset, l.bw-2*inset
		r, g, b := byte(0), byte(0), byte(0)
		if i == l.open {
			// The row whose list is open is the active one: SimUI's sub colour under its main colour.
			s.roundRect(float64(left), float64(top), float64(width), float64(l.heights[i]), o.scale*5, 0xda, 0xef, 0xff, 1)
			r, g, b = 0x1e, 0x9f, 0xff
		} else if i == l.hover && !l.inert {
			s.roundRect(float64(left), float64(top), float64(width), float64(l.heights[i]), o.scale*5, 0, 0, 0, .05)
		}
		cell := left + o.px(10)
		if l.icons {
			// A glyph may be a little wider than its cell: it is centred on it and never cut or shortened.
			o.text(s, icons, item.Icon, cell-o.px(6), top, o.px(28), l.heights[i], r, g, b, .75, iconFlags)
			cell += o.px(21)
		}
		right := left + width - o.px(10)
		if l.arrows {
			if item.Lazy || len(item.Children) > 0 {
				o.text(s, arrows, arrowGlyph, right-o.px(18), top, o.px(24), l.heights[i], r, g, b, .45, iconFlags)
			}
			right -= o.px(31)
		}
		o.text(s, text, item.Label, cell, top, right-cell, l.heights[i], r, g, b, 1, dtFlags)
	}
}

func (o *Overlay) emit(event string, path []int) {
	if o.choose != nil {
		if event == "action" {
			go o.choose(slices.Clone(path))
		}
		return
	}
	if o.host.Events != nil {
		go o.host.Events(event, o.menuID, slices.Clone(path))
	}
}

func (o *Overlay) showScreenMenu(items []Item, choose func(path []int)) {
	o.dropLevels(0, true)
	x, y := cursor()
	o.prepareAt(x, y)
	o.choose, o.screen = choose, true
	o.menuID = -1
	o.openRoot(items, x, y)
}

func (o *Overlay) showMenu(id int, items []Item, x, y float64) {
	o.dropLevels(0, o.choose != nil)
	o.choose, o.screen = nil, false
	if !o.prepare() || len(items) == 0 {
		return
	}
	o.menuID = id
	client := o.ownerClient()
	o.openRoot(items, int(client.Left)+o.px(max(1, x)), int(client.Top)+o.px(max(1, y)))
}

func (o *Overlay) openRoot(items []Item, px, py int) {
	root := &level{items: items, hover: -1, open: -1, offsetY: -10}
	// As SimUI places a dropdown: below the point, above it when there is no room, and inside the screen.
	root.place = func(w, h int) (int, int, bool) {
		area := workArea(px, py)
		flipped := py+h > int(area.Bottom)
		top := py + o.px(5)
		if flipped {
			top = py - h - o.px(5)
		}
		left := px
		if px+w > int(area.Right) {
			left = int(area.Right) - w
		}
		return max(int(area.Left)+o.px(4), min(int(area.Right)-w-o.px(4), left)), max(int(area.Top)+o.px(4), min(int(area.Bottom)-h-o.px(4), top)), flipped
	}
	if !o.build(root) {
		return
	}
	root.fade(1, 200*time.Millisecond)
	o.levels = []*level{root}
	o.opened = time.Now()
	o.foreground, _, _ = pGetForegroundWindow.Call()
	o.pressed = true // the press that asked for the menu is not one outside of it
	o.leave = time.Time{}
	o.run()
}

// dropLevels closes the lists from one depth on; told is false when the menu is replaced or a row was chosen.
func (o *Overlay) dropLevels(from int, told bool) {
	if from >= len(o.levels) {
		return
	}
	for _, l := range o.levels[from:] {
		l.closing = true
		l.fade(0, 150*time.Millisecond)
		// Under a list that is going there is nothing to click any more.
		style, _, _ := user32.NewProc("GetWindowLongPtrW").Call(l.hwnd, ^uintptr(19))
		user32.NewProc("SetWindowLongPtrW").Call(l.hwnd, ^uintptr(19), style|0x20)
		o.dying = append(o.dying, l)
	}
	o.levels = o.levels[:from]
	if from > 0 {
		o.levels[from-1].open = -1
		o.render(o.levels[from-1])
	} else if told {
		o.emit("closed", nil)
	}
	o.leave = time.Time{}
}

func (o *Overlay) openChild(depth, row int) {
	parent := o.levels[depth]
	if parent.open == row {
		return
	}
	o.dropLevels(depth+1, false)
	item := &parent.items[row]
	child := &level{items: item.Children, path: append(slices.Clone(parent.path), row), hover: -1, open: -1, offsetX: -5}
	if item.Lazy {
		child.items, child.inert = []Item{{Label: "正在读取…"}}, true
		o.emit("children", child.path)
	}
	// Beside the row: to its right, or to its left where the screen ends; its first row level with it.
	child.place = func(w, h int) (int, int, bool) {
		rowLeft, rowRight := parent.x+parent.bx+o.px(5), parent.x+parent.bx+parent.bw-o.px(5)
		rowTop := parent.y + parent.by + o.px(5) + parent.tops[row] - parent.scroll
		area := workArea(rowLeft, rowTop)
		left := rowLeft - w + o.px(2)
		if rowRight+w < int(area.Right)-o.px(4) {
			left = rowRight - o.px(2)
		}
		return max(int(area.Left)+o.px(4), left), max(int(area.Top)+o.px(4), min(int(area.Bottom)-h-o.px(4), rowTop-o.px(5))), false
	}
	if !o.build(child) {
		return
	}
	child.fade(1, 150*time.Millisecond)
	parent.open = row
	o.render(parent)
	o.levels = append(o.levels, child)
}

func (o *Overlay) setChildren(id int, path []int, items []Item) {
	if id != o.menuID {
		return
	}
	for depth, l := range o.levels {
		if depth == 0 || !slices.Equal(l.path, path) {
			continue
		}
		l.items, l.inert = items, false
		if len(items) == 0 {
			l.items, l.inert = []Item{{Label: "暂无可用操作"}}, true
		}
		alpha := l.alpha
		if o.build(l) {
			l.alpha = max(alpha, .01)
			l.fade(1, 100*time.Millisecond)
		}
		// Opening the row again in this menu shows what was read.
		parent := o.levels[depth-1]
		parent.items[path[len(path)-1]].Children, parent.items[path[len(path)-1]].Lazy = l.items, false
		if l.inert {
			parent.items[path[len(path)-1]].Children = nil
		}
		return
	}
}

func (o *Overlay) click(hwnd uintptr, x, y int) {
	for i, t := range o.toasts {
		if t.hwnd == hwnd {
			o.hideToast(i)
			return
		}
	}
	for depth, l := range o.levels {
		if l.hwnd != hwnd {
			continue
		}
		row := l.rowAt(o, l.x+x, l.y+y)
		if row < 0 || l.inert {
			return
		}
		if item := l.items[row]; item.Lazy || len(item.Children) > 0 {
			o.openChild(depth, row)
			return
		}
		o.emit("action", append(slices.Clone(l.path), row))
		o.dropLevels(0, false)
		return
	}
}

func (o *Overlay) wheel(delta int) {
	x, y := cursor()
	for depth := len(o.levels) - 1; depth >= 0; depth-- {
		if l := o.levels[depth]; l.contains(x, y) && l.contentH > l.viewH {
			o.dropLevels(depth+1, false)
			l.scroll = max(0, min(l.contentH-l.viewH, l.scroll-delta*o.px(rowHeight)/120))
			o.render(l)
			return
		}
	}
}

func (o *Overlay) tickMenu() {
	alive := o.dying[:0]
	for _, l := range o.dying {
		if l.step() {
			alive = append(alive, l)
		} else {
			l.destroy()
		}
	}
	o.dying = alive
	if len(o.levels) == 0 {
		return
	}
	for _, l := range o.levels {
		l.step()
	}
	// Another window in front, Escape, or a press anywhere else closes the menu.
	foreground, _, _ := pGetForegroundWindow.Call()
	owner := o.host.Owner()
	visible, _, _ := pIsWindowVisible.Call(owner)
	if !o.screen && (owner == 0 || visible == 0) || foreground != o.foreground || keyDown(0x1B) {
		o.dropLevels(0, true)
		return
	}
	x, y := cursor()
	depth, row := -1, -1
	for i := len(o.levels) - 1; i >= 0; i-- {
		if o.levels[i].contains(x, y) {
			depth, row = i, o.levels[i].rowAt(o, x, y)
			break
		}
	}
	down := keyDown(1) || keyDown(2) || keyDown(4)
	if down && !o.pressed && depth < 0 {
		o.dropLevels(0, true)
		return
	}
	o.pressed = down
	for i, l := range o.levels {
		hover := -1
		if i == depth {
			hover = row
		}
		if l.hover != hover {
			l.hover = hover
			o.render(l)
		}
	}
	// A row with a list of its own opens it under the pointer.
	if depth >= 0 && row >= 0 && !o.levels[depth].inert {
		if item := o.levels[depth].items[row]; item.Lazy || len(item.Children) > 0 {
			o.openChild(depth, row)
		}
	}
	// A list stays open while the pointer is on its row or in it; away from both it closes after a moment.
	keep := 1
	if depth >= 0 {
		keep = depth + 1
		if l := o.levels[depth]; l.open >= 0 && l.hover == l.open {
			keep++
		}
	}
	if len(o.levels) <= keep {
		o.leave = time.Time{}
	} else if o.leave.IsZero() {
		o.leave = time.Now()
	} else if time.Since(o.leave) > 220*time.Millisecond {
		o.dropLevels(keep, false)
	}
}

// ---------- notices ----------

// A notice is SimUI's toast: the main colour, white 16px text, 7.5px/15px padding, corners of 5px, stacked
// from 10% of the window's height above its bottom edge, 20px apart, and gone after three seconds.
type toast struct {
	*surface
	born   time.Time
	hiding bool
	bw, bh int
}

const toastMargin = 24.0

func (o *Overlay) showToast(message string) {
	if !o.prepare() || message == "" {
		return
	}
	font := o.font(false, o.px(16))
	flags := uintptr(dtFlags)
	width, height := o.textWidth(font, message), o.px(21)
	if limit := o.px(270); width > limit {
		// A long notice wraps inside the 300px a toast may take.
		chars := windows.StringToUTF16(message)
		area := rect{0, 0, int32(limit * oversample), 0}
		old, _, _ := pSelectObject.Call(o.screenDC, font)
		flags = 0x0010 | 0x0800 | 0x2000 // WORDBREAK | NOPREFIX | EDITCONTROL
		pDrawText.Call(o.screenDC, uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)-1), uintptr(unsafe.Pointer(&area)), flags|0x0400)
		pSelectObject.Call(o.screenDC, old)
		width, height = limit, (int(area.Bottom)+oversample-1)/oversample
	}
	t := &toast{born: time.Now(), bw: width + o.px(30), bh: height + o.px(15)}
	margin := o.px(toastMargin)
	// A notice belongs to the main window even while a menu of the screen is open.
	screen := o.screen
	o.screen = false
	t.surface = o.newSurface(t.bw+2*margin, t.bh+2*margin, false)
	o.screen = screen
	if t.surface == nil {
		return
	}
	t.shadow(float64(margin), float64(margin), float64(t.bw), float64(t.bh), o.scale*5, o.scale*7, o.scale*15, .1)
	t.roundRect(float64(margin), float64(margin), float64(t.bw), float64(t.bh), o.scale*5, 0x1e, 0x9f, 0xff, 1)
	o.text(t.surface, font, message, margin+o.px(15), margin+(t.bh-height)/2, width, height, 255, 255, 255, 1, flags)
	t.dy = o.px(5)
	t.fade(1, 300*time.Millisecond)
	o.toasts = append(o.toasts, t)
	if len(o.toasts) > 6 {
		o.hideToast(0)
	}
	o.placeToasts()
	o.run()
}

func (o *Overlay) hideToast(index int) {
	if t := o.toasts[index]; !t.hiding {
		t.hiding, t.closing = true, true
		t.fade(0, 200*time.Millisecond)
	}
}

// placeToasts stacks the notices over the owner, wherever that is now.
func (o *Overlay) placeToasts() {
	client := o.ownerClient()
	bottom := int(client.Bottom) - int(client.Bottom-client.Top)/10 - o.px(20)
	for _, t := range o.toasts {
		if t.hiding {
			continue
		}
		t.x = int(client.Left+client.Right)/2 - t.w/2
		t.y = bottom - t.bh - o.px(toastMargin)
		bottom -= t.bh + o.px(20)
	}
}

func (o *Overlay) tickToasts() {
	if len(o.toasts) == 0 {
		return
	}
	owner := o.host.Owner()
	visible, _, _ := pIsWindowVisible.Call(owner)
	iconic, _, _ := pIsIconic.Call(owner)
	o.placeToasts()
	alive := o.toasts[:0]
	for i, t := range o.toasts {
		if !t.hiding && time.Since(t.born) > 3*time.Second {
			o.hideToast(i)
		}
		if owner != 0 && visible != 0 && iconic == 0 && t.step() {
			alive = append(alive, t)
		} else {
			t.destroy()
		}
	}
	o.toasts = alive
}

// ---------- for the checks ----------

// Inspect reports what is shown.
func (o *Overlay) Inspect() Shot {
	done := make(chan Shot, 1)
	o.post(func() {
		shot := Shot{Menu: o.menuID, Levels: [][]string{}, Boxes: [][4]int{}, Alpha: []float64{}, Toasts: len(o.toasts)}
		for _, l := range o.levels {
			labels := []string{}
			for _, item := range l.items {
				labels = append(labels, item.Label)
			}
			shot.Levels, shot.Boxes, shot.Alpha = append(shot.Levels, labels), append(shot.Boxes, [4]int{l.x + l.bx, l.y + l.by, l.bw, l.bh}), append(shot.Alpha, l.alpha)
		}
		done <- shot
	})
	select {
	case shot := <-done:
		return shot
	case <-time.After(2 * time.Second):
		return Shot{}
	}
}

// Choose does what a click on a row does: the path names the row in the open lists.
func (o *Overlay) Choose(path []int) {
	o.post(func() {
		if len(path) == 0 {
			o.dropLevels(0, true)
			return
		}
		if depth := len(path) - 1; depth >= 0 && depth < len(o.levels) && path[depth] < len(o.levels[depth].items) {
			l := o.levels[depth]
			o.click(l.hwnd, l.bx+l.bw/2, l.by+o.px(5)+l.tops[path[depth]]+l.heights[path[depth]]/2-l.scroll)
		}
	})
}

// Dump writes the pictures of everything shown as PNG files, laid over the given grey: for looking at
// what is drawn without capturing the screen.
func (o *Overlay) Dump(dir string, grey byte) []string {
	done := make(chan []string, 1)
	o.post(func() {
		var files []string
		surfaces := []*surface{}
		for _, l := range o.levels {
			surfaces = append(surfaces, l.surface)
		}
		for _, t := range o.toasts {
			surfaces = append(surfaces, t.surface)
		}
		for i, s := range surfaces {
			picture := image.NewNRGBA(image.Rect(0, 0, s.w, s.h))
			for p := 0; p < s.w*s.h; p++ {
				a := float64(s.pix[p*4+3]) / 255
				for c := 0; c < 3; c++ {
					picture.Pix[p*4+c] = byte(float64(s.pix[p*4+2-c]) + float64(grey)*(1-a))
				}
				picture.Pix[p*4+3] = 255
			}
			name := filepath.Join(dir, fmt.Sprintf("overlay-%d-%d.png", grey, i))
			if file, err := os.Create(name); err == nil {
				png.Encode(file, picture)
				file.Close()
				files = append(files, name)
			}
		}
		done <- files
	})
	select {
	case files := <-done:
		return files
	case <-time.After(3 * time.Second):
		return nil
	}
}
