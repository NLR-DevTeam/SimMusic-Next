package core

import (
	"errors"
	"math"

	"simmusic/internal/lyrics"

	"simmusic/internal/model"
)

// Playback control as extensions see it (the "player" permission): the state of the player and the
// operations the play page offers. Song records are reduced to what is shown; cover references,
// request headers and extension data never leave the host through here.

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

// PublicTrack is the part of a song an extension with playback control may read.
func PublicTrack(t model.Track) map[string]any {
	return map[string]any{"source": t.Source, "id": t.ID, "title": t.Title, "artist": t.Artist, "album": t.Album, "duration": t.Duration, "infoRevision": t.InfoRevision}
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
	sleep := a.Sleep()
	var until int64
	if !sleep.Until.IsZero() {
		until = sleep.Until.UnixMilli()
	}
	return map[string]any{
		"current": current, "index": s.Index, "queue": queue, "lyrics": map[string]any{"traditional": lines, "perWord": perWord},
		"playing": s.Playing, "loading": s.Loading, "position": s.Pos, "duration": s.Dur,
		"volume": s.Volume, "speed": s.Speed, "mode": s.Mode,
		"sleep": map[string]any{"active": sleep.Active(), "until": until, "waitTrackEnd": sleep.WaitTrackEnd},
	}
}

// PlayerState implements sim.player.state().
func (a *App) PlayerState() map[string]any { return a.PublicState(a.Snapshot(), nil) }

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
