package webui

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	"simmusic/internal/core"
	"simmusic/internal/desklyric"
)

// Assets used by the independent native desktop-lyrics overlay and tray.
var RemixTTF, _ = files.ReadFile("web/assets/fonts/remixicon.ttf")
var IconPNG, _ = files.ReadFile("web/assets/icon.png")

func DesktopOptions(a *core.App) desklyric.Options {
	c := a.Cfg
	return desklyric.Options{Color: parseColor(c.String("dl.color")), Stroke: c.Bool("dl.border"), StrokeColor: parseColor(c.String("dl.borderColor")), Size: c.Float("dl.size"), Width: c.Float("dl.width"), Center: c.Bool("dl.center"), ShowTrans: c.Bool("dl.showTrans") && c.Bool("lrc.multiLang"), TransScale: c.Float("lrc.transSize"), HideCapture: c.Bool("dl.hideCapture"), Compact: c.Bool("dl.compact")}
}
func parseColor(s string) color.NRGBA {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	if err != nil {
		v = 0x1e9fff
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}
}

// syncSleepWindow shows the sleep timer's last-minute window over the screen while a sleep is pending.
func (u *UI) syncSleepWindow() {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	shown := false
	for {
		select {
		case <-u.quit:
			return
		case <-tick.C:
		}
		if u.hwnd.Load() == 0 {
			continue
		}
		pending := u.app.Sleep().Pending != ""
		if pending == shown {
			continue
		}
		shown = pending
		if pending {
			u.window("sleepShow")
		} else {
			u.window("sleepHide")
		}
	}
}

func (u *UI) syncDesk() {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-u.quit:
			return
		case <-tick.C:
		}
		if u.desk == nil {
			continue
		}
		s := u.app.Snapshot()
		want := u.app.State.Bool("desk.visible")
		if u.app.Sleep().Pending != "" {
			want = false
		}
		if u.app.Cfg.Bool("dl.compact") && !s.Playing {
			want = false
		}
		if want && u.playOpen.Load() && u.app.Cfg.Bool("dl.autoClose") && u.mainUncovered() {
			want = false
		}
		u.desk.Show(want)
		if !want {
			continue
		}
		u.desk.SetPlayback(s.Playing, s.Mode)
		main, sub := "", ""
		if s.Current != nil {
			main = s.Current.Title
		}
		for _, line := range s.Lyrics {
			if line.Time > s.Pos {
				break
			}
			main, sub = line.Text, line.Trans
		}
		// Word by word where the host has such lyrics: the line being sung with the times of its words.
		var words []desklyric.Word
		for _, line := range s.Words {
			if line.Start > s.Pos {
				break
			}
			main, sub = line.Text, line.Trans
			words = words[:0]
			for _, word := range line.Words {
				words = append(words, desklyric.Word{Start: word.Start, End: word.End, Text: word.Text})
			}
		}
		rate := 0.0
		if s.Playing && !s.Loading {
			rate = s.Speed
		}
		u.desk.SetLine(main, sub, words, s.Pos, rate)
	}
}

// mediaAction handles a control pressed on the taskbar preview or the system media controls.
func (u *UI) mediaAction(action string) {
	a := u.app
	switch action {
	case "previous":
		a.Prev()
	case "next":
		a.Next(false)
	case "toggle":
		a.TogglePlay()
	case "play":
		if !a.Snapshot().Playing {
			a.TogglePlay()
		}
	case "pause", "stop":
		if a.Snapshot().Playing {
			a.TogglePlay()
		}
	}
}
