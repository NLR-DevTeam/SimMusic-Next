package webui

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"simmusic/internal/core"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

// The play page in use may be one an extension offers. It is a browser of its own laid over the whole
// main window, kept loaded while it is the selected page and shown when the play page opens. It talks
// to the player through messages of its simHost object: requests go to the core directly, the state
// comes back as patches. Nothing of the host page is reachable from it; what stays the host's (the
// window buttons, menus, dialogs, notices) is drawn by the host page and cut out of the view.

type playView struct {
	id      string // the host's view; set before the first event is handled
	key     string
	plugin  *plugin.Plugin
	page    plugin.PlayPage
	base    string
	created chan struct{}
	done    chan struct{}
	jobs    chan func()

	mu    sync.Mutex
	shown bool
	side  string // what the host asked to be shown beside the player: "queue", "lyrics" or ""
	ready bool   // the page asked for the state
	sent  map[string]string
	color string
	dark  bool
	full  bool // the main window is fullscreen
	// Where the host page last placed the view; it may say so before it asks for the view to be shown.
	layout any
}

var titleColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func playPageKey(p *plugin.Plugin, page plugin.PlayPage) string { return p.ID() + "/" + page.ID }

// playPages lists the pages on offer: those of running extensions granted the play page permission and playback control.
func (u *UI) playPages() []map[string]any {
	out := []map[string]any{}
	for _, p := range u.app.Plugins.All() {
		if !p.PlayPagesAllowed() {
			continue
		}
		for _, page := range p.Manifest.PlayPages {
			out = append(out, map[string]any{"key": playPageKey(p, page), "plugin": p.ID(), "pluginName": p.Manifest.Name, "builtin": p.Builtin, "id": page.ID,
				"name": page.Name, "description": page.Description, "preview": page.Preview != ""})
		}
	}
	return out
}

// The page of the bundled extension: the one in use until the user picks another.
const defaultPlayPage = "playPage/default"

// selectedPlayPage is the page in use: the one the user chose, else the bundled one, else any page
// on offer. Pages of an extension whose page failed to load are passed over.
func (u *UI) selectedPlayPage() (*plugin.Plugin, plugin.PlayPage, bool) {
	h := &u.webviews
	h.mu.Lock()
	failed := h.playFailed
	h.mu.Unlock()
	for _, wanted := range []string{u.app.Cfg.String("play.page"), defaultPlayPage, ""} {
		for _, p := range u.app.Plugins.All() {
			if !p.PlayPagesAllowed() || p == failed {
				continue
			}
			for _, page := range p.Manifest.PlayPages {
				if wanted == "" || playPageKey(p, page) == wanted {
					return p, page, true
				}
			}
		}
	}
	return nil, plugin.PlayPage{}, false
}

func (u *UI) livePlayView() *playView {
	h := &u.webviews
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.play
}

// activePlayPage tells the host page which extension page stands in for the built-in one.
func (u *UI) activePlayPage() any {
	pv := u.livePlayView()
	if pv == nil {
		return nil
	}
	pv.mu.Lock()
	defer pv.mu.Unlock()
	return map[string]any{"key": pv.key, "background": pv.color, "dark": pv.dark, "windowButtons": pv.page.WindowButtons}
}

func (u *UI) playViewKey() string {
	if pv := u.livePlayView(); pv != nil {
		return pv.key
	}
	return ""
}

// syncPlayView keeps the browser of the selected page alive and closes one that is no longer it.
func (u *UI) syncPlayView() {
	h := &u.webviews
	h.syncMu.Lock()
	defer h.syncMu.Unlock()
	p, page, ok := u.selectedPlayPage()
	current := u.livePlayView()
	if current != nil && (!ok || current.plugin != p || current.key != playPageKey(p, page)) {
		u.dropPlayView(current)
		current = nil
	}
	if !ok || current != nil {
		return
	}
	base, err := u.extBase(p)
	if err != nil {
		return
	}
	pv := &playView{key: playPageKey(p, page), plugin: p, page: page, base: base, created: make(chan struct{}), done: make(chan struct{}),
		jobs: make(chan func(), 64), sent: map[string]string{}, color: page.Background, dark: page.Dark}
	if pv.color == "" {
		pv.color = "#ffffff"
		if page.Dark {
			pv.color = "#000000"
		}
	}
	id, err := u.OpenWebview(p, plugin.WebviewOptions{Kind: "player", URL: base + strings.TrimLeft(page.URL, "/"), Background: pv.color}, pv.sink(u))
	if err != nil {
		return
	}
	pv.id = id
	h.mu.Lock()
	h.play = pv
	h.mu.Unlock()
	close(pv.created)
	go pv.run(u)
}

// dropPlayView forgets a view at once, so that the built-in page serves from now on, and closes it.
func (u *UI) dropPlayView(pv *playView) {
	h := &u.webviews
	h.mu.Lock()
	if h.play == pv {
		h.play = nil
	}
	h.mu.Unlock()
	u.hostSend("close", pv.id)
}

func (u *UI) failPlayView(pv *playView, reason string) {
	h := &u.webviews
	h.mu.Lock()
	live := h.play == pv
	if live {
		h.playFailed = pv.plugin
	}
	h.mu.Unlock()
	if !live {
		return
	}
	pv.plugin.Logf("播放页 %s 加载失败：%s", pv.page.ID, reason)
	u.dropPlayView(pv)
	u.Alert("所选播放页无法加载，已改用其他可用的播放页。")
}

// run does the page's requests in order and delivers the state while the page is shown.
func (pv *playView) run(u *UI) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for tick := 0; ; tick++ {
		select {
		case job := <-pv.jobs:
			job()
		case <-ticker.C:
			// A hidden page shows nothing: it is brought up to date once a second, and at once when it is shown.
			pv.mu.Lock()
			shown := pv.shown
			pv.mu.Unlock()
			if shown || tick%4 == 0 {
				pv.push(u, false)
			}
		case <-pv.done:
			return
		case <-u.quit:
			return
		}
	}
}

func (pv *playView) sink(u *UI) func(string, []string) {
	return func(event string, args []string) {
		<-pv.created
		arg := func(i int) string {
			if i < len(args) {
				return args[i]
			}
			return ""
		}
		switch event {
		case "start":
			pv.mu.Lock()
			pv.ready = false
			pv.mu.Unlock()
		case "load":
			if status, _ := strconv.Atoi(arg(1)); status >= 400 {
				u.failPlayView(pv, "HTTP "+arg(1))
			}
		case "fail":
			u.failPlayView(pv, arg(2))
		case "fullscreen":
			pv.mu.Lock()
			pv.full = arg(0) == "1"
			pv.mu.Unlock()
			select {
			case pv.jobs <- func() { pv.push(u, false) }:
			default:
			}
		case "message":
			address, text := arg(0), arg(1)
			select {
			case pv.jobs <- func() { pv.receive(u, address, text) }:
			default: // a page flooding the host loses requests instead of holding the event reader up
			}
		case "close":
			h := &u.webviews
			h.mu.Lock()
			if h.play == pv {
				h.play = nil
			}
			h.mu.Unlock()
			select {
			case <-pv.done:
			default:
				close(pv.done)
			}
		}
	}
}

// post hands the page one message of the player channel.
func (pv *playView) post(u *UI, message string) {
	u.hostSend("eval", pv.id, `window.__simHostReceive&&window.__simHostReceive("m",`+jsString(message)+`)`)
}

// receive handles one message of the page. Only the extension's own pages are heard: a page of
// another origin loaded into the view cannot control the player.
func (pv *playView) receive(u *UI, address, text string) {
	if !strings.HasPrefix(address, pv.base) {
		return
	}
	var request struct {
		Sim    string `json:"sim"`
		ID     any    `json:"id"`
		Action string `json:"action"`
		Args   []any  `json:"args"`
	}
	if json.Unmarshal([]byte(text), &request) != nil || request.Sim != "player" {
		// Anything else is a message for the extension itself.
		var data any
		if json.Unmarshal([]byte(text), &data) == nil {
			go pv.plugin.Call(10*time.Second, "onPlayPageMessage", pv.page.ID, data)
		}
		return
	}
	result, err := pv.perform(u, request.Action, request.Args)
	reply := map[string]any{"sim": "player", "reply": request.ID, "result": result}
	if err != nil {
		reply = map[string]any{"sim": "player", "reply": request.ID, "error": err.Error()}
	}
	if encoded, err := json.Marshal(reply); err == nil && request.ID != nil {
		pv.post(u, string(encoded))
	}
}

func (pv *playView) perform(u *UI, action string, args []any) (any, error) {
	if !pv.plugin.PlayPagesAllowed() {
		return nil, errors.New("权限被拒绝：播放页或播放控制未授权")
	}
	switch action {
	case "hello":
		pv.mu.Lock()
		pv.ready = true
		pv.sent = map[string]string{}
		pv.mu.Unlock()
		pv.push(u, true)
	case "close":
		u.playRequest(map[string]any{"kind": "close"})
	case "pointer":
	case "showMenu":
		// The song menu of the play page, or with { scene: "queue", index } that of one song of the queue.
		request := map[string]any{"kind": "songMenu", "scene": "play"}
		if options, _ := append(args, nil)[0].(map[string]any); options["scene"] == "queue" {
			request["scene"], request["index"] = "queue", options["index"]
		}
		u.playRequest(request)
	case "key":
		if event, _ := append(args, nil)[0].(map[string]any); event != nil {
			u.playRequest(map[string]any{"kind": "key", "event": event})
		}
	case "window":
		if name, _ := append(args, "")[0].(string); name == "minimize" || name == "maximize" || name == "close" {
			u.window(name)
		}
	case "setFullscreen":
		// The main window fullscreen, for the page that is the play page shown now.
		pv.mu.Lock()
		shown := pv.shown
		pv.mu.Unlock()
		if !shown {
			return nil, errors.New("播放页未展开，不能切换全屏")
		}
		on, _ := append(args, nil)[0].(bool)
		if !on {
			u.hostSend("eval", pv.id, "document.fullscreenElement&&document.exitFullscreen()")
		}
		u.hostSend("fullscreen", pv.id, flag(on))
	case "setLyricsShown":
		shown, _ := append(args, nil)[0].(bool)
		u.app.State.Set("lrcShow", shown)
		u.playRequest(map[string]any{"kind": "lyricsShown", "value": shown})
	case "spectrum":
		// Asked for every frame of a visualization: answered without a delivery of the state.
		bands := 64
		if value, ok := append(args, nil)[0].(float64); ok {
			bands = int(value)
		}
		return u.app.PlayerSpectrum(bands), nil
	case "setQueue", "append", "playNext":
		refs, err := plugin.QueueRefs(append(args, nil)[0])
		if err != nil {
			return nil, err
		}
		options, err := plugin.ParseQueueOptions(append(args, nil, nil)[1])
		if err != nil {
			return nil, err
		}
		op := map[string]string{"setQueue": "set", "append": "append", "playNext": "playNext"}[action]
		if err := u.app.PlayerQueue(pv.plugin, op, refs, options); err != nil {
			return nil, err
		}
		pv.push(u, false)
	case "loadInfo":
		var indexes []int
		list, _ := append(args, nil)[0].([]any)
		for _, item := range list {
			if index, ok := item.(float64); ok {
				indexes = append(indexes, int(index))
			}
		}
		u.app.PlayerLoadInfo(indexes)
	case "showDialog":
		name, _ := append(args, "")[0].(string)
		if name != "equalizer" && name != "sleep" && name != "speed" {
			return nil, errors.New("未知的对话框：" + name)
		}
		u.playRequest(map[string]any{"kind": "dialog", "name": name})
	case "setTitleBar":
		options, _ := append(args, nil)[0].(map[string]any)
		pv.mu.Lock()
		if color, _ := options["color"].(string); titleColor.MatchString(color) {
			pv.color = color
		}
		if dark, ok := options["dark"].(bool); ok {
			pv.dark = dark
		}
		request := map[string]any{"kind": "titleBar", "background": pv.color, "dark": pv.dark}
		pv.mu.Unlock()
		u.playRequest(request)
	default:
		result, err := u.app.PlayerCommand(action, args)
		if err == nil {
			pv.push(u, false)
		}
		return result, err
	}
	return true, nil
}

// pageTrack adds the addresses the page loads the song's covers from, on its own origin.
func pageTrack(t model.Track) map[string]any {
	out := core.PublicTrack(t)
	address := "/.sim/cover?key=" + url.QueryEscape(t.Key()) + "&rev=" + strconv.FormatUint(t.InfoRevision, 10)
	out["cover"], out["largeCover"] = address, address+"&scene=play"
	return out
}

// A play page is told what sim.player tells a script, in the same groups, and besides that what concerns
// it as a view: the window it is in with the status, and whether and how it is shown as "view".
var playStateGroups = func() []plugin.PlayerGroup {
	groups := append([]plugin.PlayerGroup(nil), plugin.PlayerGroups...)
	for i, group := range groups {
		if group.Name == "status" {
			groups[i].Keys = append(append([]string(nil), group.Keys...), "windowMaximized", "fullscreen")
		}
	}
	return append(groups, plugin.PlayerGroup{Name: "view", Keys: []string{"visible", "side"}})
}()

// push sends the page what changed since the last delivery. A hidden page stays up to date, so that
// it is right the moment it is shown, but is not told the position as it advances.
func (pv *playView) push(u *UI, force bool) {
	pv.mu.Lock()
	defer pv.mu.Unlock()
	if !pv.ready {
		return
	}
	snapshot := u.app.Snapshot()
	u.remember(snapshot.Queue)
	// A song is shown with what the index knows of it by now, as the lists of the host page do.
	u.mu.Lock()
	state := u.app.PublicState(snapshot, func(t model.Track) map[string]any {
		if known, ok := u.tracks[t.Key()]; ok && known.InfoRevision >= t.InfoRevision {
			t = known
		}
		return pageTrack(t)
	})
	u.mu.Unlock()
	state["visible"], state["side"] = pv.shown, pv.side
	state["windowMaximized"], state["fullscreen"] = u.maximized(), pv.full
	patch := map[string]any{}
	changed := []string{}
	for _, group := range playStateGroups {
		part := map[string]any{}
		for _, key := range group.Keys {
			part[key] = state[key]
		}
		encoded, _ := json.Marshal(part)
		if pv.sent[group.Name] == string(encoded) {
			continue
		}
		pv.sent[group.Name] = string(encoded)
		changed = append(changed, group.Name)
		for key, value := range part {
			patch[key] = value
		}
	}
	position := strconv.FormatFloat(snapshot.Pos, 'f', 3, 64)
	if len(changed) == 0 && (pv.sent["position"] == position || !pv.shown && !force) {
		return
	}
	pv.sent["position"] = position
	patch["position"] = snapshot.Pos
	if encoded, err := json.Marshal(map[string]any{"sim": "player", "event": "state", "state": patch, "changed": changed}); err == nil {
		pv.post(u, string(encoded))
	}
	// A page that is not being drawn (closed, or its window out of sight) draws for a moment for another
	// song: what it loads for it is ready when the page is next seen.
	for _, group := range changed {
		if group == "track" || group == "queue" || group == "lyrics" {
			u.hostSend("stir", pv.id)
			break
		}
	}
}

// PostPlayPage implements sim.playPage.postMessage.
func (u *UI) PostPlayPage(p *plugin.Plugin, data string) bool {
	pv := u.livePlayView()
	if pv == nil || pv.plugin != p {
		return false
	}
	pv.mu.Lock()
	ready := pv.ready
	pv.mu.Unlock()
	if ready {
		pv.post(u, `{"sim":"player","event":"message","data":`+data+`}`)
	}
	return ready
}

// showPlayView shows or hides the extension page; false tells the host page to use the built-in one.
func (u *UI) showPlayView(shown bool, side string) bool {
	pv := u.livePlayView()
	if pv == nil {
		return false
	}
	if side != "queue" && side != "lyrics" {
		side = ""
	}
	pv.mu.Lock()
	appeared := shown && !pv.shown
	pv.shown, pv.side = shown, side
	layout := pv.layout
	pv.mu.Unlock()
	if !shown {
		u.hostSend("layout", pv.id, "0", "0", "0", "0", "0")
		u.hostSend("eval", pv.id, "document.fullscreenElement&&document.exitFullscreen()")
		u.hostSend("fullscreen", pv.id, "0")
	} else if appeared && layout != nil {
		// The page is told first: the host lets it draw what it now shows before sliding it into view.
		pv.push(u, true)
		u.hostSend(layoutFields(pv.id, layout)...)
	}
	select {
	case pv.jobs <- func() { pv.push(u, true) }:
	default:
	}
	return true
}

func (u *UI) playLayout(value any) {
	pv := u.livePlayView()
	if pv == nil {
		return
	}
	pv.mu.Lock()
	pv.layout = value
	shown := pv.shown
	pv.mu.Unlock()
	if shown {
		u.hostSend(layoutFields(pv.id, value)...)
	}
}

// playRequest queues something for the host page to do at once: what a play page asks of it (closing it, a
// menu, a dialog, a key), and what happens in a menu the host drew for it.
func (u *UI) playRequest(request map[string]any) {
	u.mu.Lock()
	u.playRequests = append(u.playRequests, request)
	if len(u.playRequests) > 32 {
		u.playRequests = u.playRequests[len(u.playRequests)-32:]
	}
	u.mu.Unlock()
	select {
	case u.playSignal <- struct{}{}:
	default:
	}
}

// playWait returns the queued requests, waiting for one; the host page asks again as soon as it has them.
func (u *UI) playWait() any {
	deadline := time.After(20 * time.Second)
	for {
		u.mu.Lock()
		requests := u.playRequests
		u.playRequests = nil
		u.mu.Unlock()
		if len(requests) > 0 {
			return requests
		}
		select {
		case <-u.playSignal:
		case <-deadline:
			return []any{}
		case <-u.quit:
			return []any{}
		}
	}
}

// playPreview serves the picture an extension shows for one of its play pages in the settings.
func (u *UI) playPreview(w http.ResponseWriter, r *http.Request) {
	p := u.app.Plugins.Get(r.URL.Query().Get("plugin"))
	if p != nil {
		for _, page := range p.Manifest.PlayPages {
			if page.ID != r.URL.Query().Get("id") || page.Preview == "" {
				continue
			}
			data, err := fs.ReadFile(p.Source(), page.Preview)
			kind := http.DetectContentType(data)
			if strings.HasSuffix(strings.ToLower(page.Preview), ".svg") {
				// Shown as a picture only: nothing in it runs.
				kind = "image/svg+xml"
			}
			if err != nil || len(data) > 8<<20 || kind != "image/png" && kind != "image/jpeg" && kind != "image/webp" && kind != "image/gif" && kind != "image/svg+xml" {
				break
			}
			w.Header().Set("Content-Type", kind)
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
			return
		}
	}
	http.NotFound(w, r)
}
