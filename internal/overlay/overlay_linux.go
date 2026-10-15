// Package overlay on Linux: the context menus and notices are windows of the CEF host, which has the
// toolkit to draw them. This side only describes them to the host and passes on what happens in them.
//
// Commands (fields of one line, see Host.Send):
//
//	menu show <id> <screen 0|1> <x> <y> <font family> {<path> <flags> <icon> <label>}...
//	menu children <id> <path> {<path> <flags> <icon> <label>}...
//	menu close
//	toast <text> <font family>
//
// A path is the row's indices from the top list down, joined by dots; flags are 1 separator, 2 lazy,
// 4 has children. x and y are CSS pixels of the main window's client area. The host answers with
// "action", "children" and "closed" events, handed to HostEvent.
package overlay

import (
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Overlay is the set of menu and notice windows.
type Overlay struct {
	host Host

	mu     sync.Mutex
	menuID int
	choose func(path []int) // set for a menu whose rows act here, not in the host page
	items  []Item           // the rows of the open menu
	open   bool
	toasts int
}

// screenMenu is the id the host knows a menu of the screen (the tray's) by.
const screenMenu = -1

// Start prepares the overlay; the host draws once it runs.
func Start(host Host) *Overlay { return &Overlay{host: host} }

func (o *Overlay) send(fields ...string) {
	if o.host.Send != nil {
		o.host.Send(fields...)
	}
}

func (o *Overlay) family() string {
	if o.host.Font != nil {
		return o.host.Font()
	}
	return ""
}

func joinPath(path []int) string {
	parts := make([]string, len(path))
	for i, index := range path {
		parts[i] = strconv.Itoa(index)
	}
	return strings.Join(parts, ".")
}

func splitPath(text string) []int {
	path := []int{}
	for _, part := range strings.Split(text, ".") {
		if index, err := strconv.Atoi(part); err == nil {
			path = append(path, index)
		}
	}
	return path
}

// rows lists the items depth first, each with its path.
func rows(fields []string, parent []int, items []Item) []string {
	for i, item := range items {
		path := append(slices.Clone(parent), i)
		flags := 0
		if item.Separator {
			flags |= 1
		}
		if item.Lazy {
			flags |= 2
		}
		if len(item.Children) > 0 {
			flags |= 4
		}
		fields = append(fields, joinPath(path), strconv.Itoa(flags), item.Icon, item.Label)
		fields = rows(fields, path, item.Children)
	}
	return fields
}

func (o *Overlay) show(id int, items []Item, choose func(path []int)) {
	o.mu.Lock()
	o.menuID, o.items, o.choose, o.open = id, items, choose, true
	o.mu.Unlock()
	fields := []string{"menu", "show", strconv.Itoa(id), o.family()}
	o.send(rows(fields, nil, items)...)
}

// ShowMenu opens a menu of the host page where the pointer is, whether or not the main window is shown.
func (o *Overlay) ShowMenu(id int, items []Item) { o.show(id, items, nil) }

// ShowMenuAtPointer opens such a menu whose rows act here instead of in the host page:
// the menu of the tray icon. choose is called with the path of the row chosen.
func (o *Overlay) ShowMenuAtPointer(items []Item, choose func(path []int)) {
	o.show(screenMenu, items, choose)
}

// itemAt finds the row a path names.
func itemAt(items []Item, path []int) *Item {
	var found *Item
	for _, index := range path {
		if index < 0 || index >= len(items) {
			return nil
		}
		found = &items[index]
		items = found.Children
	}
	return found
}

// SetChildren supplies the rows of a lazy row that was opened.
func (o *Overlay) SetChildren(id int, path []int, items []Item) {
	o.mu.Lock()
	current := o.open && id == o.menuID
	if current {
		if row := itemAt(o.items, path); row != nil {
			row.Children = items
		}
	}
	o.mu.Unlock()
	if current {
		o.send(rows([]string{"menu", "children", strconv.Itoa(id), joinPath(path)}, path, items)...)
	}
}

// Toast shows a short notice above the bottom of the main window.
func (o *Overlay) Toast(text string) {
	o.mu.Lock()
	o.toasts++
	o.mu.Unlock()
	o.send("toast", text, o.family())
}

// HostEvent takes what the host reports of its menus: "action <id> <path>", "children <id> <path>",
// "closed <id>" and "toastGone".
func (o *Overlay) HostEvent(fields []string) {
	if len(fields) == 0 {
		return
	}
	if fields[0] == "toastGone" {
		o.mu.Lock()
		if o.toasts > 0 {
			o.toasts--
		}
		o.mu.Unlock()
		return
	}
	if len(fields) < 2 {
		return
	}
	id, err := strconv.Atoi(fields[1])
	o.mu.Lock()
	if err != nil || !o.open || id != o.menuID {
		o.mu.Unlock()
		return
	}
	path := []int{}
	if len(fields) > 2 {
		path = splitPath(fields[2])
	}
	event, choose := fields[0], o.choose
	if event == "action" || event == "closed" {
		o.open, o.items, o.choose = false, nil, nil
	}
	o.mu.Unlock()
	if choose != nil {
		// A menu of this program's own: only the row chosen matters.
		if event == "action" {
			go choose(path)
		}
		return
	}
	if o.host.Events != nil && (event == "action" || event == "children" || event == "closed") {
		if event == "closed" {
			path = nil
		}
		go o.host.Events(event, id, path)
	}
}

// ---------- for the checks ----------

// Inspect reports what is shown: the rows of the open menu's top list, as this side knows them.
func (o *Overlay) Inspect() Shot {
	o.mu.Lock()
	defer o.mu.Unlock()
	shot := Shot{Menu: o.menuID, Levels: [][]string{}, Boxes: [][4]int{}, Alpha: []float64{}, Toasts: o.toasts}
	if o.open {
		labels := []string{}
		for _, item := range o.items {
			labels = append(labels, item.Label)
		}
		shot.Levels, shot.Boxes, shot.Alpha = append(shot.Levels, labels), append(shot.Boxes, [4]int{}), append(shot.Alpha, 1)
	}
	return shot
}

// Choose does what a click on a row does; an empty path dismisses the menu.
func (o *Overlay) Choose(path []int) {
	o.mu.Lock()
	id, open := o.menuID, o.open
	o.mu.Unlock()
	if !open {
		return
	}
	o.send("menu", "close")
	if len(path) == 0 {
		o.HostEvent([]string{"closed", strconv.Itoa(id)})
	} else {
		o.HostEvent([]string{"action", strconv.Itoa(id), joinPath(path)})
	}
}

// Dump writes pictures of what is shown on Windows; the host's windows cannot be read from here.
func (o *Overlay) Dump(dir string, grey byte) []string { return nil }
