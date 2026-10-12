package lyrics

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Word-by-word lyrics. What a source supplies is LRC text; where its lines carry the inline time tags
// of enhanced LRC (<mm:ss.xx> before each word, optionally one more after the last), the words have
// exact times. Where they do not, a line can still be given words: its time is shared out evenly.

// Word is one timed piece of a line: a word, a syllable or a single character, spaces included.
type Word struct {
	Start, End float64
	Text       string
}

// WordLine is one line of word-by-word lyrics.
type WordLine struct {
	Start, End float64
	Text       string
	Trans      string
	Words      []Word
}

// How lines without word times of their own are treated.
const (
	WordsSource = "source" // as the lyrics are: only lines that carry word times have words
	WordsExtend = "extend" // lyrics that carry word times anywhere get words on every line
	WordsAll    = "all"    // every song gets words on every line
)

var wordTagRe = regexp.MustCompile(`<(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?>`)

func tagTime(line string, m []int) float64 {
	min, _ := strconv.Atoi(line[m[2]:m[3]])
	sec, _ := strconv.Atoi(line[m[4]:m[5]])
	frac := 0.0
	if m[6] >= 0 {
		digits := line[m[6]:m[7]]
		value, _ := strconv.Atoi(digits)
		frac = float64(value)
		for range len(digits) {
			frac /= 10
		}
	}
	return float64(min*60+sec) + frac
}

// entry is one timed line of the source, with its text as written (word tags included).
type entry struct {
	time float64
	text string
	seq  int
}

func entries(src string) []entry {
	var all []entry
	for _, line := range strings.Split(strings.ReplaceAll(src, "\r", ""), "\n") {
		tags := tagRe.FindAllStringSubmatchIndex(line, -1)
		// 时间标签必须连续位于行首
		end := 0
		var times []float64
		for _, m := range tags {
			if m[0] != end {
				break
			}
			end = m[1]
			times = append(times, tagTime(line, m))
		}
		text := strings.TrimSpace(line[end:])
		for _, t := range times {
			all = append(all, entry{t, text, len(all)})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].time != all[j].time {
			return all[i].time < all[j].time
		}
		return all[i].seq < all[j].seq
	})
	return all
}

// plain is a line's text without its word tags.
func plain(text string) string {
	return strings.TrimSpace(wordTagRe.ReplaceAllString(text, ""))
}

// exactWords reads the words of a line that carries word tags; nil when it carries none. A tag gives
// the start of the text after it and the end of the text before it; text before the first tag begins
// with the line. The last word ends with a closing tag, or at zero for the caller to fill in.
func exactWords(text string, start float64) []Word {
	tags := wordTagRe.FindAllStringSubmatchIndex(text, -1)
	if len(tags) == 0 {
		return nil
	}
	var words []Word
	if lead := text[:tags[0][0]]; strings.TrimSpace(lead) != "" {
		words = append(words, Word{Start: start, End: tagTime(text, tags[0]), Text: lead})
	}
	for i, m := range tags {
		next := len(text)
		if i+1 < len(tags) {
			next = tags[i+1][0]
		}
		piece := text[m[1]:next]
		if piece == "" || strings.TrimSpace(piece) == "" {
			// A tag with nothing after it only closes the word before it.
			if len(words) > 0 && strings.TrimSpace(piece) == "" && i+1 == len(tags) {
				words[len(words)-1].End = tagTime(text, m)
			} else if len(words) > 0 {
				words[len(words)-1].Text += piece
			}
			continue
		}
		word := Word{Start: tagTime(text, m), Text: piece}
		if i+1 < len(tags) {
			word.End = tagTime(text, tags[i+1])
		}
		words = append(words, word)
	}
	return words
}

// wide reports the characters that are sung one by one: Han, kana, hangul.
func wide(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// pieces cuts a line into what is sung one after the other: a word of letters with the spaces after it, or
// a single wide character; punctuation stays with what it follows.
func pieces(text string) []string {
	var out []string
	current := ""
	content, letters := false, false // the piece holds something sung; it is a word of letters
	flush := func() {
		out = append(out, current)
		current, content, letters = "", false, false
	}
	for _, r := range text {
		switch {
		case wide(r):
			if content {
				flush()
			}
			current += string(r)
			content = true
		case unicode.IsSpace(r):
			current += string(r)
			if letters {
				flush()
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’' || r == '-':
			if content && !letters {
				flush()
			}
			current += string(r)
			content, letters = true, true
		default: // punctuation stays with what it follows, or leads what comes next
			current += string(r)
		}
	}
	if content {
		flush()
	} else if current != "" && len(out) > 0 {
		out[len(out)-1] += current
	} else if strings.TrimSpace(current) != "" {
		out = append(out, current)
	}
	return out
}

// evenWords shares a line's time out evenly among its pieces.
func evenWords(text string, start, end float64) []Word {
	parts := pieces(text)
	if len(parts) == 0 {
		return nil
	}
	step := (end - start) / float64(len(parts))
	words := make([]Word, len(parts))
	for i, part := range parts {
		words[i] = Word{Start: start + step*float64(i), End: start + step*float64(i+1), Text: part}
	}
	return words
}

// ParseWords reads word-by-word lyrics from LRC text, or returns nil when, by the mode, the song has
// none: its lyrics carry no word times and the mode does not make them up. multiLang merges lines of the
// same time into text and translation, as Parse does. duration is the song's, for the end of the last line.
//
// A line ends where the next one begins. A line of the source that has a time but no text is not a line
// of lyrics: it only says where the line before it ends. Without such a mark, a line whose word times are
// made up is not stretched over a long gap: it takes at most a second for each of its pieces.
func ParseWords(src string, multiLang bool, mode string, duration float64) []WordLine {
	all := entries(src)
	type parsed struct {
		line   WordLine
		exact  bool
		marked bool // its end is given by the source: a closing tag or an empty line
	}
	var lines []parsed
	anyExact := false
	for i, e := range all {
		text := plain(e.text)
		if text == "" {
			continue
		}
		if multiLang && len(lines) > 0 && lines[len(lines)-1].line.Start == e.time && i > 0 && all[i-1].time == e.time {
			if lines[len(lines)-1].line.Trans == "" {
				lines[len(lines)-1].line.Trans = text
			}
			continue
		}
		p := parsed{line: WordLine{Start: e.time, Text: text}}
		// The end: the next time in the source, an empty line's included.
		for _, later := range all[i+1:] {
			if later.time > e.time {
				p.line.End, p.marked = later.time, plain(later.text) == ""
				break
			}
		}
		if p.line.End == 0 {
			p.line.End = e.time + 5
			if duration > e.time {
				p.line.End = duration
			}
		}
		if words := exactWords(e.text, e.time); len(words) > 0 {
			p.exact, anyExact = true, true
			if last := &words[len(words)-1]; last.End > 0 {
				p.line.End, p.marked = last.End, true
			} else {
				last.End = p.line.End
			}
			p.line.Words = words
		}
		lines = append(lines, p)
	}
	if len(lines) == 0 || mode != WordsAll && !anyExact {
		return nil
	}
	out := make([]WordLine, len(lines))
	for i, p := range lines {
		if !p.exact {
			if mode == WordsSource {
				// As the lyrics are: the line has no words of its own, it is one piece.
				p.line.Words = []Word{{Start: p.line.Start, End: p.line.End, Text: p.line.Text}}
			} else {
				end := p.line.End
				if !p.marked {
					end = min(end, p.line.Start+float64(len(pieces(p.line.Text))))
				}
				p.line.End = end
				p.line.Words = evenWords(p.line.Text, p.line.Start, end)
			}
		}
		out[i] = p.line
	}
	return out
}
