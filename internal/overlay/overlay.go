package overlay

// Item is one row of a menu. Icon is the glyph itself, in the icon font.
type Item struct {
	Label     string `json:"label"`
	Icon      string `json:"icon"`
	Separator bool   `json:"separator"`
	Children  []Item `json:"children"`
	// The children are asked for when the row is opened: Events gets "children", SetChildren answers.
	Lazy bool `json:"lazy"`
}

// Host is what the overlay needs of the application.
type Host struct {
	Owner    func() uintptr // the main window; zero while there is none
	Font     func() string  // the interface font family
	IconFont []byte         // RemixIcon
	// Events reports "action" (a row was chosen), "children" (a lazy row was opened) and "closed",
	// with the menu's id and the path of the row. Called on a goroutine of its own.
	Events func(event string, menu int, path []int)
	// Send hands a command to the CEF host. Only used where the host draws the menus and notices (Linux);
	// on Windows this package draws them itself.
	Send func(fields ...string)
}

// Shot is what the overlay shows at one moment.
type Shot struct {
	Menu   int        `json:"menu"`
	Levels [][]string `json:"levels"` // the labels of each open list
	Boxes  [][4]int   `json:"boxes"`  // their place on the screen
	Alpha  []float64  `json:"alpha"`
	Toasts int        `json:"toasts"`
}
