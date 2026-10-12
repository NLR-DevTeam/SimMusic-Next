package webui

import (
	"log"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"simmusic/internal/core"
)

// The system media controls on Linux are MPRIS: the program is a player on the session bus, the desktop
// (its media keys, the panel's player widget, the lock screen) reads what is playing and calls its controls.

const (
	mprisPath   = dbus.ObjectPath("/org/mpris/MediaPlayer2")
	mprisRoot   = "org.mpris.MediaPlayer2"
	mprisPlayer = "org.mpris.MediaPlayer2.Player"
)

type mprisRootObject struct{ u *UI }

func (o mprisRootObject) Raise() *dbus.Error { o.u.ShowWindow(true); return nil }
func (o mprisRootObject) Quit() *dbus.Error  { o.u.Quit(); return nil }

type mprisPlayerObject struct{ u *UI }

func (o mprisPlayerObject) Next() *dbus.Error      { go o.u.mediaAction("next"); return nil }
func (o mprisPlayerObject) Previous() *dbus.Error  { go o.u.mediaAction("previous"); return nil }
func (o mprisPlayerObject) Pause() *dbus.Error     { go o.u.mediaAction("pause"); return nil }
func (o mprisPlayerObject) PlayPause() *dbus.Error { go o.u.mediaAction("toggle"); return nil }
func (o mprisPlayerObject) Stop() *dbus.Error      { go o.u.mediaAction("stop"); return nil }
func (o mprisPlayerObject) Play() *dbus.Error      { go o.u.mediaAction("play"); return nil }

// Seek moves by an offset, SetPosition to a place, both in microseconds.
func (o mprisPlayerObject) Seek(offset int64) *dbus.Error {
	go func() {
		s := o.u.app.Snapshot()
		o.u.app.Seek(max(0, min(s.Dur, s.Pos+float64(offset)/1e6)))
	}()
	return nil
}
func (o mprisPlayerObject) SetPosition(track dbus.ObjectPath, position int64) *dbus.Error {
	go func() {
		s := o.u.app.Snapshot()
		o.u.app.Seek(max(0, min(s.Dur, float64(position)/1e6)))
	}()
	return nil
}

// syncMedia publishes the player and keeps what it shows current.
func (u *UI) syncMedia() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Println("mpris:", err)
		return
	}
	defer conn.Close()
	root, player := mprisRootObject{u}, mprisPlayerObject{u}
	writable := func(apply func(value any)) func(*prop.Change) *dbus.Error {
		return func(change *prop.Change) *dbus.Error { go apply(change.Value); return nil }
	}
	props, err := prop.Export(conn, mprisPath, prop.Map{
		mprisRoot: {
			"CanQuit":             {Value: true, Emit: prop.EmitTrue},
			"CanRaise":            {Value: true, Emit: prop.EmitTrue},
			"HasTrackList":        {Value: false, Emit: prop.EmitTrue},
			"Identity":            {Value: "SimMusic", Emit: prop.EmitTrue},
			"DesktopEntry":        {Value: "SimMusic.Next", Emit: prop.EmitTrue},
			"SupportedUriSchemes": {Value: []string{"file"}, Emit: prop.EmitTrue},
			"SupportedMimeTypes":  {Value: []string{"audio/mpeg", "audio/flac", "audio/ogg", "audio/x-wav"}, Emit: prop.EmitTrue},
		},
		mprisPlayer: {
			"PlaybackStatus": {Value: "Stopped", Emit: prop.EmitTrue},
			"LoopStatus": {Value: "Playlist", Writable: true, Emit: prop.EmitTrue, Callback: writable(func(value any) {
				// The player's modes are one setting: looping the list, looping the song, shuffling.
				mode := core.ModeLoop
				if value == "Track" {
					mode = core.ModeRepeat
				}
				u.setMode(mode)
			})},
			"Shuffle": {Value: false, Writable: true, Emit: prop.EmitTrue, Callback: writable(func(value any) {
				mode := core.ModeLoop
				if value == true {
					mode = core.ModeShuffle
				}
				u.setMode(mode)
			})},
			"Rate":        {Value: 1.0, Emit: prop.EmitTrue},
			"MinimumRate": {Value: 1.0, Emit: prop.EmitTrue},
			"MaximumRate": {Value: 1.0, Emit: prop.EmitTrue},
			"Metadata":    {Value: map[string]dbus.Variant{"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack"))}, Emit: prop.EmitTrue},
			"Volume": {Value: 1.0, Writable: true, Emit: prop.EmitTrue, Callback: writable(func(value any) {
				if volume, ok := value.(float64); ok {
					u.app.SetVolume(max(0, min(1, volume)))
				}
			})},
			// The position is read when wanted; it is not announced as it advances.
			"Position":      {Value: int64(0), Emit: prop.EmitFalse},
			"CanGoNext":     {Value: true, Emit: prop.EmitTrue},
			"CanGoPrevious": {Value: true, Emit: prop.EmitTrue},
			"CanPlay":       {Value: true, Emit: prop.EmitTrue},
			"CanPause":      {Value: true, Emit: prop.EmitTrue},
			"CanSeek":       {Value: true, Emit: prop.EmitTrue},
			"CanControl":    {Value: true, Emit: prop.EmitFalse},
		},
	})
	if err != nil {
		log.Println("mpris:", err)
		return
	}
	node := &introspect.Node{Name: string(mprisPath), Interfaces: []introspect.Interface{
		introspect.IntrospectData, prop.IntrospectData,
		{Name: mprisRoot, Methods: introspect.Methods(root), Properties: props.Introspection(mprisRoot)},
		{Name: mprisPlayer, Methods: introspect.Methods(player), Properties: props.Introspection(mprisPlayer),
			Signals: []introspect.Signal{{Name: "Seeked", Args: []introspect.Arg{{Name: "Position", Type: "x", Direction: "out"}}}}},
	}}
	for _, export := range []struct {
		object any
		name   string
	}{{root, mprisRoot}, {player, mprisPlayer}, {introspect.NewIntrospectable(node), "org.freedesktop.DBus.Introspectable"}} {
		if err = conn.Export(export.object, mprisPath, export.name); err != nil {
			log.Println("mpris:", err)
			return
		}
	}
	// A second instance of the program never gets this far, so the name is free unless another build runs.
	name := "org.mpris.MediaPlayer2.simmusic"
	if reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		if reply, err = conn.RequestName(name+".instance"+strconv.Itoa(os.Getpid()), dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
			log.Println("mpris: the bus name is taken")
			return
		}
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	last := map[string]string{}
	// set publishes a property when the text standing for its value has changed.
	set := func(property, key string, value any) {
		if last[property] != key {
			last[property] = key
			props.SetMust(mprisPlayer, property, value)
		}
	}
	lastPosition := 0.0
	for {
		select {
		case <-u.quit:
			return
		case <-tick.C:
		}
		s := u.app.Snapshot()
		status := "Stopped"
		metadata := map[string]dbus.Variant{"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack"))}
		key := ""
		if t := s.Current; t != nil {
			status = "Paused"
			if s.Playing {
				status = "Playing"
			}
			metadata = map[string]dbus.Variant{
				"mpris:trackid": dbus.MakeVariant(dbus.ObjectPath("/org/simmusic/track/" + strconv.Itoa(s.Index))),
				"mpris:length":  dbus.MakeVariant(int64(s.Dur * 1e6)),
				"xesam:title":   dbus.MakeVariant(t.Title),
			}
			if t.Artist != "" && t.Artist != "未知艺术家" {
				metadata["xesam:artist"] = dbus.MakeVariant([]string{t.Artist})
			}
			if t.Album != "" {
				metadata["xesam:album"] = dbus.MakeVariant(t.Album)
			}
			cover := ""
			if t.InfoReady || t.Cover != "" {
				u.remember(s.Queue)
				cover = u.url + "cover?key=" + url.QueryEscape(t.Key()) + "&scene=play&rev=" + strconv.FormatUint(t.InfoRevision, 10)
				metadata["mpris:artUrl"] = dbus.MakeVariant(cover)
			}
			key = t.Key() + "\x00" + t.Title + "\x00" + t.Artist + "\x00" + t.Album + "\x00" + cover + "\x00" + strconv.Itoa(s.Index) + "\x00" + strconv.FormatFloat(s.Dur, 'f', 1, 64)
		}
		set("PlaybackStatus", status, status)
		set("Metadata", key, metadata)
		loop := "Playlist"
		if s.Mode == core.ModeRepeat {
			loop = "Track"
		}
		set("LoopStatus", loop, loop)
		set("Shuffle", strconv.FormatBool(s.Mode == core.ModeShuffle), s.Mode == core.ModeShuffle)
		set("Volume", strconv.FormatFloat(s.Volume, 'f', 2, 64), s.Volume)
		props.SetMust(mprisPlayer, "Position", int64(s.Pos*1e6))
		// A jump in the position, as opposed to its steady advance, is announced.
		if step := s.Pos - lastPosition; step < -0.5 || step > 1.5 {
			_ = conn.Emit(mprisPath, mprisPlayer+".Seeked", int64(s.Pos*1e6))
		}
		lastPosition = s.Pos
	}
}

// setMode cycles the player to the given mode: the core only offers stepping through its modes.
func (u *UI) setMode(mode int) {
	for i := 0; i < 3 && u.app.Snapshot().Mode != mode; i++ {
		u.app.CycleMode()
	}
}
