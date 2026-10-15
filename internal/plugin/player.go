package plugin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dop251/goja"

	"simmusic/internal/model"
)

// Playback control (the "player" permission) is one interface with two homes: sim.player in the script of
// an extension, simPlayer in a play page. Both hold the same state, announce the same events and offer the
// same operations; a play page adds only what concerns it as a view (being shown and closed, the window,
// menus and dialogs).

// PlayerHost is the playback control of the host (implemented by core).
type PlayerHost interface {
	// PlayerState is everything a player display is told: the song, the queue, the lyrics, the status and
	// the settings it goes by.
	PlayerState() map[string]any
	PlayerCommand(name string, args []any) (any, error)
	// PlayerCover is the cover picture of a song in the state, the current one for an empty source and id.
	PlayerCover(source, id string, large bool) []byte
	// PlayerLoadInfo reads, in the background, the information of queue positions the index lacks.
	PlayerLoadInfo(indexes []int)
	// PlayerSpectrum is the level of bands frequency bands of what is being heard, each 0..1.
	PlayerSpectrum(bands int) []float64
	// PlayerQueue puts songs into the queue for p: op is "set", "append" or "playNext".
	PlayerQueue(p *Plugin, op string, tracks []model.Track, options QueueOptions) error
	// PlayerPlayback is the audio a song of the queue is played from, the current one for an empty source
	// and id: a file (Path) or an address with its request headers (URL, Headers).
	PlayerPlayback(source, id string) (model.Resource, error)
}

// QueueOptions say how a queue that is set goes on: Index is the song made current (-1: the current one when
// the new queue holds it, the first otherwise); Position and Play, when given, where it plays from and whether.
type QueueOptions struct {
	Index    int
	Position *float64
	Play     *bool
}

// QueueRefs reads the songs a script names: { source?, id } each.
func QueueRefs(value any) ([]model.Track, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, errors.New("歌曲列表应为数组")
	}
	refs := make([]model.Track, len(list))
	for i, item := range list {
		fields, _ := item.(map[string]any)
		refs[i].Source, _ = fields["source"].(string)
		refs[i].ID, _ = fields["id"].(string)
	}
	return refs, nil
}

// ParseQueueOptions reads { index, position, play } of setQueue.
func ParseQueueOptions(value any) (QueueOptions, error) {
	options := QueueOptions{Index: -1}
	fields, _ := value.(map[string]any)
	number := func(v any) (float64, bool) {
		switch n := v.(type) {
		case float64:
			return n, !math.IsNaN(n) && !math.IsInf(n, 0)
		case int64:
			return float64(n), true
		}
		return 0, false
	}
	if v, ok := fields["index"]; ok && v != nil {
		index, ok := number(v)
		if !ok || index != math.Trunc(index) || index < 0 {
			return options, errors.New("队列位置超出范围")
		}
		options.Index = int(index)
	}
	if v, ok := number(fields["position"]); ok {
		options.Position = &v
	}
	if v, ok := fields["play"].(bool); ok {
		options.Play = &v
	}
	return options, nil
}

// PlayPageBackend delivers a message of the extension to its play page while that page is loaded.
type PlayPageBackend interface {
	PostPlayPage(p *Plugin, data string) bool
}

// PlayerCommands are the operations on the player.
var PlayerCommands = []string{"toggle", "play", "pause", "previous", "next", "stop", "seek", "setVolume", "setSpeed", "cycleMode", "setMode",
	"playIndex", "removeFromQueue", "moveInQueue", "clearQueue", "setSleep", "cancelSleep", "showWindow", "showMenu"}

// PlayerGroup is one event of the player and the fields of the state it stands for.
type PlayerGroup struct {
	Name string
	Keys []string
}

// PlayerGroups are the events in the order they are announced. A change of any field of a group is one
// event of that name; "position" and "change" follow every delivery.
var PlayerGroups = []PlayerGroup{
	{"track", []string{"current", "index"}},
	{"queue", []string{"queue"}},
	{"lyrics", []string{"lyrics"}},
	{"status", []string{"playing", "loading", "duration", "volume", "speed", "mode", "sleep", "settings"}},
}

var playerEvents = []string{"track", "queue", "lyrics", "status", "position", "change"}

// playerClock carries the position on between the host's reports, as a play page's simPlayer does: it runs
// at the playback speed, a report only steers it, and it starts afresh when a report is far from it.
type playerClock struct {
	position, rate, duration, reported float64
	at                                 time.Time
	running                            bool
}

func (c *playerClock) now() float64 { return c.position + time.Since(c.at).Seconds()*c.rate }

func (c *playerClock) steer(state map[string]any) {
	number := func(key string) float64 { v, _ := state[key].(float64); return v }
	playing, _ := state["playing"].(bool)
	loading, _ := state["loading"].(bool)
	speed := number("speed")
	c.running, c.reported, c.duration = playing && !loading, number("position"), number("duration")
	now := c.now()
	off := c.reported - now
	if !c.running || c.rate == 0 || math.Abs(off) > .3 {
		c.position, c.rate = c.reported, 0
		if c.running {
			c.rate = speed
		}
	} else {
		c.position, c.rate = now, speed*(1+math.Max(-.1, math.Min(.1, off*.5)))
	}
	c.at = time.Now()
}

func (c *playerClock) read() float64 {
	if !c.running || c.rate == 0 {
		return c.reported
	}
	limit := c.duration
	if limit <= 0 {
		limit = math.Inf(1)
	}
	return math.Max(0, math.Min(limit, c.now()))
}

func (p *Plugin) installPlayer(rs *runState, sim *goja.Object) {
	rt := p.rt
	host := func() PlayerHost {
		p.need(PermPlayer)
		h, ok := p.mgr.Host.(PlayerHost)
		if !ok {
			p.throw(errors.New("当前宿主不支持播放控制"))
		}
		return h
	}
	player := rt.NewObject()
	sim.Set("player", player)
	state := rt.NewObject()
	listeners := map[string][]goja.Value{}
	clock := &playerClock{at: time.Now()}
	parse, _ := goja.AssertFunction(rt.Get("JSON").ToObject(rt).Get("parse"))

	// What was delivered last, one JSON text per group. Whoever holds sentMu reads the host.
	var sentMu sync.Mutex
	sent := map[string]string{}
	// diff reads the host's state and returns what differs from the last delivery.
	diff := func(h PlayerHost) (full map[string]any, patch string, changed []string, moved bool) {
		sentMu.Lock()
		defer sentMu.Unlock()
		full = h.PlayerState()
		part := map[string]any{}
		for _, group := range PlayerGroups {
			fields := map[string]any{}
			for _, key := range group.Keys {
				fields[key] = full[key]
			}
			encoded, _ := json.Marshal(fields)
			if sent[group.Name] == string(encoded) {
				continue
			}
			sent[group.Name] = string(encoded)
			changed = append(changed, group.Name)
			for key, value := range fields {
				part[key] = value
			}
		}
		position, _ := json.Marshal(full["position"])
		moved = sent["position"] != string(position)
		sent["position"] = string(position)
		part["position"] = full["position"]
		encoded, _ := json.Marshal(part)
		return full, string(encoded), changed, moved
	}
	// apply runs on the extension's thread: the state object takes the new values, then the events fire.
	apply := func(full map[string]any, patch string, changed []string, announce bool) {
		if fresh, err := parse(goja.Undefined(), rt.ToValue(patch)); err == nil {
			object := fresh.ToObject(rt)
			for _, key := range object.Keys() {
				state.Set(key, object.Get(key))
			}
		}
		clock.steer(full)
		if !announce {
			return
		}
		fire := func(kind string, argument goja.Value) {
			for _, handler := range append([]goja.Value(nil), listeners[kind]...) {
				if fn, ok := goja.AssertFunction(handler); ok {
					if _, err := fn(player, argument); err != nil {
						p.logf("播放控制 %s 事件处理出错: %v", kind, err)
					}
				}
			}
		}
		for _, group := range changed {
			fire(group, state)
		}
		fire("position", state)
		detail := rt.NewObject()
		detail.Set("state", state)
		detail.Set("changed", changed)
		fire("change", detail)
		_, _ = rt.RunString("void 0")
	}
	// The state is first read when the script first uses the player; from then on the host is looked at
	// four times a second, and at once after an operation of this extension.
	following := false
	kick := make(chan struct{}, 1)
	follow := func() {
		h := host()
		if following {
			return
		}
		following = true
		full, patch, changed, _ := diff(h)
		apply(full, patch, changed, false)
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-rs.quit:
					return
				case <-ticker.C:
				case <-kick:
				}
				if !p.HasPermission(PermPlayer) {
					continue
				}
				full, patch, changed, moved := diff(h)
				if len(changed) == 0 && !moved {
					continue
				}
				if !p.postState(rs, func() { apply(full, patch, changed, true) }) {
					return
				}
			}
		}()
	}
	// state is the live state: { current, index, queue, lyrics, playing, loading, position, duration,
	// volume, speed, mode, sleep, settings }. The object stays the same one; its fields are replaced.
	player.DefineAccessorProperty("state", rt.ToValue(func() goja.Value { follow(); return state }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	// position() is the position now: the reports arrive four times a second, the time since is added here.
	player.Set("position", func() float64 { follow(); return clock.read() })
	known := func(name string) string {
		for _, event := range playerEvents {
			if event == name {
				return name
			}
		}
		p.throw(errors.New("未知的播放控制事件：" + name))
		return ""
	}
	// Events: track, queue, lyrics, status, position (each with the state), change ({ state, changed }).
	player.Set("on", func(name string, handler goja.Value) *goja.Object {
		follow()
		if _, ok := goja.AssertFunction(handler); ok {
			listeners[known(name)] = append(listeners[name], handler)
		}
		return player
	})
	player.Set("off", func(name string, handler goja.Value) *goja.Object {
		list := listeners[known(name)]
		for i, item := range list {
			if item.SameAs(handler) {
				listeners[name] = append(append([]goja.Value(nil), list[:i]...), list[i+1:]...)
				break
			}
		}
		return player
	})
	// Operations may call back into extensions (a song being opened), so none runs on this thread.
	for _, name := range PlayerCommands {
		player.Set(name, func(call goja.FunctionCall) goja.Value {
			h := host()
			args := make([]any, len(call.Arguments))
			for i, arg := range call.Arguments {
				args[i] = exportJSON(arg)
			}
			return p.async(func() (any, error) {
				result, err := h.PlayerCommand(name, args)
				select {
				case kick <- struct{}{}:
				default:
				}
				return result, err
			})
		})
	}
	// setQueue(tracks, { index, position, play }) replaces the queue; append(tracks) adds to its end;
	// playNext(tracks) places songs after the current one. Songs are { source?, id }: of a protocol of the
	// extension's own, or held by the queue already. A current song the new queue keeps plays on undisturbed.
	queueOp := func(op string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			h := host()
			refs, err := QueueRefs(exportJSON(call.Argument(0)))
			if err != nil {
				p.throw(err)
			}
			options, err := ParseQueueOptions(exportJSON(call.Argument(1)))
			if err != nil {
				p.throw(err)
			}
			return p.async(func() (any, error) {
				err := h.PlayerQueue(p, op, refs, options)
				select {
				case kick <- struct{}{}:
				default:
				}
				return err == nil, err
			})
		}
	}
	player.Set("setQueue", queueOp("set"))
	player.Set("append", queueOp("append"))
	player.Set("playNext", queueOp("playNext"))
	// playback(track?) is the audio a song of the queue is played from, the current one when no song is
	// given: Promise<File | { url, headers }>, as getMusicInfo hands it over in "playback".
	player.Set("playback", func(call goja.FunctionCall) goja.Value {
		h := host()
		var source, id string
		if track, _ := exportJSON(call.Argument(0)).(map[string]any); track != nil {
			source, _ = track["source"].(string)
			id, _ = track["id"].(string)
		}
		return p.async(func() (any, error) {
			r, err := h.PlayerPlayback(source, id)
			if err != nil {
				return nil, err
			}
			if r.URL != "" {
				headers := r.Headers
				if headers == nil {
					headers = map[string]string{}
				}
				return map[string]any{"url": r.URL, "headers": headers}, nil
			}
			info, err := os.Stat(r.Path)
			if err != nil {
				return nil, err
			}
			return jsBuilder(func(rt *goja.Runtime) goja.Value {
				file := rt.NewObject()
				file.SetPrototype(rt.Get("File").ToObject(rt).Get("prototype").ToObject(rt))
				file.Set("path", r.Path)
				file.Set("name", filepath.Base(r.Path))
				file.Set("size", info.Size())
				file.Set("lastModified", info.ModTime().UnixMilli())
				return file
			}), nil
		})
	})
	// loadInfo(indexes) asks for the information of songs of the queue that have none yet (infoReady false);
	// it arrives with a "queue" event.
	player.Set("loadInfo", func(indexes []int) goja.Value {
		h := host()
		return p.async(func() (any, error) { h.PlayerLoadInfo(indexes); return true, nil })
	})
	// spectrum(bands?) is the level of the frequency bands of what is being heard: Promise<number[]>, each 0..1,
	// 64 bands unless told otherwise. Asked for as often as a visualization draws; nothing is announced.
	player.Set("spectrum", func(call goja.FunctionCall) goja.Value {
		h := host()
		bands := 64
		if value, ok := exportJSON(call.Argument(0)).(float64); ok {
			bands = int(value)
		}
		return p.async(func() (any, error) { return h.PlayerSpectrum(bands), nil })
	})
	// cover(track?, { large }) reads the cover picture of a song of the state, the current one when no song
	// is given: Promise<File | null>. A script has no use for an address; the File is the picture itself.
	player.Set("cover", func(call goja.FunctionCall) goja.Value {
		h := host()
		var source, id string
		if track, _ := exportJSON(call.Argument(0)).(map[string]any); track != nil {
			source, _ = track["source"].(string)
			id, _ = track["id"].(string)
		}
		options, _ := exportJSON(call.Argument(1)).(map[string]any)
		large := options["large"] == true
		return p.async(func() (any, error) {
			data := h.PlayerCover(source, id, large)
			if len(data) == 0 {
				return nil, nil
			}
			return jsBuilder(func(rt *goja.Runtime) goja.Value {
				kind := http.DetectContentType(data)
				extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "image/bmp": ".bmp"}[kind]
				file := rt.NewObject()
				file.SetPrototype(rt.Get("File").ToObject(rt).Get("prototype").ToObject(rt))
				file.Set("data", base64.StdEncoding.EncodeToString(data))
				file.Set("name", "cover"+extension)
				file.Set("type", kind)
				file.Set("size", len(data))
				return file
			}), nil
		})
	})
	// sim.playPage.postMessage(data) reaches the extension's own play page while it is the one in use;
	// the page's simPlayer.postMessage arrives at the exported onPlayPageMessage(pageId, data).
	page := rt.NewObject()
	sim.Set("playPage", page)
	page.Set("postMessage", func(data goja.Value) bool {
		p.need(PermPlayer)
		backend, ok := p.mgr.webviewBackend().(PlayPageBackend)
		if !ok {
			return false
		}
		text := "null"
		if data != nil && !goja.IsUndefined(data) {
			stringify, _ := goja.AssertFunction(rt.Get("JSON").ToObject(rt).Get("stringify"))
			v, err := stringify(goja.Undefined(), data)
			if err != nil {
				panic(err)
			}
			if !goja.IsUndefined(v) {
				text = v.String()
			}
		}
		return backend.PostPlayPage(p, text)
	})
}
