package lyrics

import (
	"math"
	"reflect"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func texts(words []Word) []string {
	out := make([]string, len(words))
	for i, word := range words {
		out[i] = word.Text
	}
	return out
}

const plainSong = "[00:10.00]第一句歌词\n[00:14.00]\n[00:20.00]Hello brave new world\n[00:20.00]你好，新世界\n[00:50.00]最后一句"
const wordSong = "[00:10.00]<00:10.00>第<00:10.50>一<00:11.00>句<00:11.80>\n[00:20.00]没有逐字的一行\n[00:23.00]\n[00:30.00]<00:30.00>Hello <00:30.60>world\n[00:40.00]尾声"

func TestTraditionalIgnoresEmptyLinesAndWordTags(t *testing.T) {
	lines := Parse(wordSong, true)
	got := []string{}
	for _, line := range lines {
		got = append(got, line.Text)
	}
	if !reflect.DeepEqual(got, []string{"第一句", "没有逐字的一行", "Hello world", "尾声"}) || lines[2].Time != 30 {
		t.Fatalf("lines: %v", lines)
	}
	if lines := Parse(plainSong, true); len(lines) != 3 || lines[1].Trans != "你好，新世界" {
		t.Fatalf("plain lines: %v", lines)
	}
}

func TestWordsAsTheLyricsAre(t *testing.T) {
	// Lyrics without word times have no word-by-word form unless every song is to get one.
	if ParseWords(plainSong, true, WordsSource, 60) != nil || ParseWords(plainSong, true, WordsExtend, 60) != nil {
		t.Fatal("word times were made up for lyrics that carry none")
	}
	lines := ParseWords(wordSong, true, WordsSource, 60)
	if len(lines) != 4 {
		t.Fatalf("lines: %v", lines)
	}
	first := lines[0]
	if !reflect.DeepEqual(texts(first.Words), []string{"第", "一", "句"}) || !near(first.Words[1].Start, 10.5) || !near(first.Words[2].End, 11.8) || !near(first.End, 11.8) || first.Text != "第一句" {
		t.Fatalf("exact line: %+v", first)
	}
	// A line without word times stays one piece; the empty line after it says where it ends.
	if second := lines[1]; len(second.Words) != 1 || second.Words[0].Text != "没有逐字的一行" || !near(second.End, 23) {
		t.Fatalf("line without word times: %+v", second)
	}
	// Without a closing tag the last word lasts until the next line.
	if third := lines[2]; !reflect.DeepEqual(texts(third.Words), []string{"Hello ", "world"}) || !near(third.Words[1].End, 40) {
		t.Fatalf("open last word: %+v", third)
	}
}

func TestWordsExtendedToEveryLine(t *testing.T) {
	lines := ParseWords(wordSong, true, WordsExtend, 60)
	second := lines[1]
	// Seven characters share the three seconds up to the empty line evenly.
	if len(second.Words) != 7 || !near(second.Words[0].Start, 20) || !near(second.Words[0].End, 20+3.0/7) || !near(second.Words[6].End, 23) {
		t.Fatalf("shared out: %+v", second)
	}
	// The last line has nothing after it: its end is not marked, so its two characters take a second each.
	if last := lines[3]; len(last.Words) != 2 || !near(last.End, 42) {
		t.Fatalf("last line: %+v", last)
	}
}

func TestWordsForEverySong(t *testing.T) {
	lines := ParseWords(plainSong, true, WordsAll, 60)
	if len(lines) != 3 {
		t.Fatalf("lines: %v", lines)
	}
	// Marked by the empty line: five characters over four seconds.
	if first := lines[0]; len(first.Words) != 5 || !near(first.End, 14) || !near(first.Words[4].Start, 10+4*0.8) {
		t.Fatalf("first: %+v", first)
	}
	// Words of letters are pieces with the space after them; the translation comes along.
	if second := lines[1]; !reflect.DeepEqual(texts(second.Words), []string{"Hello ", "brave ", "new ", "world"}) || second.Trans != "你好，新世界" || !near(second.End, 24) {
		t.Fatalf("second: %+v", second)
	}
	if got := pieces("「你好」，world! 再见"); !reflect.DeepEqual(got, []string{"「你", "好」，", "world! ", "再", "见"}) {
		t.Fatalf("pieces: %q", got)
	}
}
