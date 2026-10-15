package core

import (
	"errors"
	"math"
	"strings"
	"time"

	"simmusic/internal/lyrics"

	"simmusic/internal/model"
)

// Playback control as extensions see it (the "player" permission): the state of the player and the
// operations on it, the same for the script of an extension (sim.player) and for a play page (simPlayer).
// Song records are reduced to what is shown; cover references and extension data never leave the host
// through here: a cover is handed over as the picture itself. The audio of a queued song is, on request of a
// script (sim.player.playback), as the file or the address with its request headers it is played from.

// Lyrics as extensions and play pages get them: never the text a source supplied, always what the
// host parsed from it, in the host's own shape. One field holds both kinds:
//
//	lyrics.traditional  line by line: [{ time, text, translation }]
//	lyrics.perWord      word by word: [WordLine], or null when the song has none
//
// Both are read by the host from the LRC text a source supplies (package lyrics). Word times come from
// the inline tags of enhanced LRC; by the "lrc.perWord" setting, lines without them are left as one piece,
// or have their time shared out evenly among their words. The shape is what the formats in use have in
// common once parsed (enhanced LRC, QRC, KRC, YRC, Lyricify Syllable, TTML): lines with a start and an
// end, made of timed pieces of text, with what some of them add: a translation, a romanization, a second
// voice, a background line; the latter three are not read from anything yet. Times are seconds.
type WordLine struct {
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	Text         string  `json:"text"` // the whole line: the pieces' text put together
	Translation  string  `json:"translation"`
	Romanization string  `json:"romanization"`
	Background   bool    `json:"background"` // a backing line sung with the line before it
	Voice        int     `json:"voice"`      // 0 for the main singer, 1 for the other one of a duet
	Words        []Word  `json:"words"`
}

// Word is one timed piece of a line: a word, a syllable or a single character, spaces included in its text.
type Word struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// PublicTrack is the part of a song an extension with playback control may read. infoReady says whether the
// index holds the song's information yet; until it does the title and the rest may be incomplete.
func PublicTrack(t model.Track) map[string]any {
	return map[string]any{"source": t.Source, "id": t.ID, "title": t.Title, "artist": t.Artist, "album": t.Album, "duration": t.Duration,
		"infoRevision": t.InfoRevision, "infoReady": t.InfoReady || t.Cover != ""}
}

// PublicState turns a snapshot into the state object of sim.player and of play pages.
// track decides how one song is represented, PublicTrack when nil.
func (a *App) PublicState(s Snapshot, track func(model.Track) map[string]any) map[string]any {
	if track == nil {
		track = PublicTrack
	}
	queue := make([]any, len(s.Queue))
	for i, t := range s.Queue {
		queue[i] = track(t)
	}
	lines := make([]any, len(s.Lyrics))
	for i, line := range s.Lyrics {
		lines[i] = map[string]any{"time": line.Time, "text": line.Text, "translation": line.Trans}
	}
	var perWord []WordLine
	for _, line := range s.Words {
		words := make([]Word, len(line.Words))
		for i, word := range line.Words {
			words[i] = Word{Start: word.Start, End: word.End, Text: word.Text}
		}
		perWord = append(perWord, WordLine{Start: line.Start, End: line.End, Text: line.Text, Translation: line.Trans, Words: words})
	}
	var current any
	if s.Current != nil {
		current = track(*s.Current)
	}
	// The settings a player display goes by: those of the play interface and of the lyrics, and the interface font.
	settings := map[string]any{}
	if a.Cfg != nil {
		for key, value := range a.Cfg.All() {
			if key != "play.page" && (strings.HasPrefix(key, "play.") || strings.HasPrefix(key, "lrc.") || key == "general.font") {
				settings[key] = value
			}
		}
	}
	sleep := a.Sleep()
	var until int64
	if !sleep.Until.IsZero() {
		until = sleep.Until.UnixMilli()
	}
	return map[string]any{
		"current": current, "index": s.Index, "queue": queue, "lyrics": map[string]any{"traditional": lines, "perWord": perWord},
		"playing": s.Playing, "loading": s.Loading, "position": s.Pos, "duration": s.Dur,
		"volume": s.Volume, "speed": s.Speed, "mode": s.Mode, "settings": settings,
		"sleep": map[string]any{"active": sleep.Active(), "until": until, "waitTrackEnd": sleep.WaitTrackEnd},
	}
}

// PlayerState is the state of sim.player: all a play page is told of the playback, in the same shape.
func (a *App) PlayerState() map[string]any { return a.PublicState(a.Snapshot(), nil) }

// PlayerCover is the picture data of the cover of a song in the player's state, the current one when source
// and id are empty; large asks for the cover of the play page. nil when there is no such song or it has no
// cover. An extension gets the picture itself, never where it came from.
func (a *App) PlayerCover(source, id string, large bool) []byte {
	s := a.Snapshot()
	track := s.Current
	if source != "" || id != "" {
		track = nil
		for i := range s.Queue {
			if s.Queue[i].Source == source && s.Queue[i].ID == id {
				track = &s.Queue[i]
				break
			}
		}
	}
	if track == nil {
		return nil
	}
	t := *track
	if fresh, err := a.GetMusicInfo([]model.Track{t}, "list"); err == nil && len(fresh) == 1 {
		// The large cover is known once the song has been opened; the list record does not carry it.
		if fresh[0].LargeCover == "" || fresh[0].LargeCover == fresh[0].Cover {
			fresh[0].LargeCover, fresh[0].LargeCoverHeaders, fresh[0].LargeCoverProvider, fresh[0].LargeCoverExpires = t.LargeCover, t.LargeCoverHeaders, t.LargeCoverProvider, t.LargeCoverExpires
		}
		t = fresh[0]
	}
	if large && t.LargeCover != "" && (t.LargeCoverExpires <= 0 || t.LargeCoverExpires > float64(time.Now().UnixMilli())/1000) {
		t.Cover, t.CoverHeaders, t.CoverProvider = t.LargeCover, t.LargeCoverHeaders, t.LargeCoverProvider
	}
	return a.CoverBytes(t)
}

// PlayerSpectrum is the spectrum of what is being heard: bands levels from 0 to 1, from low to high.
func (a *App) PlayerSpectrum(bands int) []float64 {
	if a.Engine == nil {
		return make([]float64, max(1, min(256, bands)))
	}
	return a.Engine.Spectrum(bands)
}

// PlayerLoadInfo reads the information of the songs at these positions of the queue that the index does not
// hold yet, in the background: the state shows it once it is there.
func (a *App) PlayerLoadInfo(indexes []int) {
	queue := a.Snapshot().Queue
	var missing []model.Track
	a.mu.Lock()
	if a.infoLoading == nil {
		a.infoLoading = map[string]bool{}
	}
	for _, index := range indexes {
		if index < 0 || index >= len(queue) || len(missing) >= 200 {
			continue
		}
		t := queue[index]
		if !t.InfoReady && t.Cover == "" && !a.infoLoading[t.Key()] {
			a.infoLoading[t.Key()] = true
			missing = append(missing, t)
		}
	}
	a.mu.Unlock()
	if len(missing) == 0 {
		return
	}
	go func() {
		_, err := a.GetMusicInfo(missing, "list")
		a.mu.Lock()
		for _, t := range missing {
			delete(a.infoLoading, t.Key())
		}
		a.mu.Unlock()
		if err != nil {
			a.Alert(a.MusicInfoFailed(missing, err).Error())
		}
	}()
}

// PlayerCommand runs one playback operation by name, with positional arguments as JSON gave them.
func (a *App) PlayerCommand(name string, args []any) (any, error) {
	number := func(i int) (float64, error) {
		if i < len(args) {
			switch v := args[i].(type) {
			case float64:
				if !math.IsNaN(v) && !math.IsInf(v, 0) {
					return v, nil
				}
			case int64:
				return float64(v), nil
			case int:
				return float64(v), nil
			}
		}
		return 0, errors.New("参数应为数字")
	}
	switch name {
	case "toggle":
		a.TogglePlay()
	case "play", "pause":
		if a.Snapshot().Playing != (name == "play") {
			a.TogglePlay()
		}
	case "previous":
		a.Prev()
	case "next":
		a.Next(false)
	case "stop":
		a.Stop()
	case "seek":
		v, err := number(0)
		if err != nil {
			return nil, err
		}
		a.Seek(max(0, v))
	case "setVolume":
		v, err := number(0)
		if err != nil {
			return nil, err
		}
		a.SetVolume(max(0, min(1, v)))
	case "setSpeed":
		v, err := number(0)
		if err != nil {
			return nil, err
		}
		a.SetSpeed(max(0.1, min(10, v)))
	case "cycleMode":
		return a.CycleMode(), nil
	case "setMode":
		v, err := number(0)
		if err != nil || v != math.Trunc(v) || v < 0 || v > 2 {
			return nil, errors.New("播放模式应为 0、1 或 2")
		}
		for i := 0; i < 3 && a.Mode() != int(v); i++ {
			a.CycleMode()
		}
		return a.Mode(), nil
	case "playIndex", "removeFromQueue":
		v, err := number(0)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		n := len(a.queue)
		a.mu.Unlock()
		if v != math.Trunc(v) || v < 0 || int(v) >= n {
			return nil, errors.New("队列位置超出范围")
		}
		if name == "playIndex" {
			a.PlayIndex(int(v), true)
		} else {
			a.RemoveFromQueue(int(v))
		}
	case "moveInQueue":
		from, err := number(0)
		if err != nil {
			return nil, err
		}
		to, err := number(1)
		if err != nil {
			return nil, err
		}
		a.MoveQueue(int(from), int(to))
	case "clearQueue":
		a.ClearQueue()
	case "setSleep":
		v, err := number(0)
		if err != nil || v <= 0 || v > 1440 {
			return nil, errors.New("定时时间应在 0 至 1440 分钟之间")
		}
		a.SetSleep(v)
	case "cancelSleep":
		a.ExitSleep()
	case "showWindow":
		// The main window, shown and brought to the front: for a control outside it (a taskbar or desktop widget).
		if a.OnShowWindow != nil {
			a.OnShowWindow()
		}
	case "showMenu":
		// The play page's menu of the current song, where the pointer is: for a control outside the window.
		if a.Snapshot().Current == nil {
			return false, nil
		}
		if a.OnSongMenu != nil {
			a.OnSongMenu()
		}
	default:
		return nil, errors.New("未知的播放控制操作：" + name)
	}
	return true, nil
}

// MigratePlayPageSettings hands the appearance settings the play page had while it was part of the
// main interface to the bundled play page extension, once: what the user had chosen is kept.
func (a *App) MigratePlayPageSettings() {
	p := a.Plugins.Get("playPage")
	if p == nil {
		return
	}
	for old, key := range map[string]string{"play.dark": "dark", "play.dynamicBg": "dynamicBg", "play.effect3D": "effect3D", "play.coverScale": "coverScale",
		"lrc.blur": "lyricsBlur", "lrc.align": "lyricsAlign", "lrc.size": "lyricsSize", "lrc.spacing": "lyricsSpacing"} {
		value := a.Cfg.Get(old)
		if value == nil {
			continue
		}
		if p.Settings.Get(key) == nil {
			p.Settings.Set(key, value)
		}
		a.Cfg.Delete(old)
	}
}

// lyricWords is the word-by-word reading of the current lyrics, by the "lrc.perWord" setting. It is read
// from the text once and kept: the state is asked for several times a second.
func (a *App) lyricWords(duration float64) []lyrics.WordLine {
	if a.Cfg == nil {
		return nil
	}
	mode, multi := a.Cfg.String("lrc.perWord"), a.Cfg.Bool("lrc.multiLang")
	a.mu.Lock()
	defer a.mu.Unlock()
	if w := &a.words; w.raw != a.lyricsRaw || w.mode != mode || w.multi != multi || w.duration != duration {
		w.raw, w.mode, w.multi, w.duration = a.lyricsRaw, mode, multi, duration
		w.lines = lyrics.ParseWords(a.lyricsRaw, multi, mode, duration)
	}
	return a.words.lines
}
