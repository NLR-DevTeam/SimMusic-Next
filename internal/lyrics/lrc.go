// Package lyrics 解析 LRC 歌词。
package lyrics

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Line struct {
	Time  float64 // 秒
	Text  string
	Trans string // 翻译（多语言模式下时间戳相同的第二行）
}

var tagRe = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// Parse 解析 LRC 文本。multiLang 为 true 时，时间戳相同的多行合并为原文 + 翻译。
// 行内的逐字时间标签（增强 LRC 的 <mm:ss.xx>）不属于文本；只有时间、没有内容的行不是歌词，完全忽略。
func Parse(src string, multiLang bool) []Line {
	type raw struct {
		t   float64
		txt string
		seq int
	}
	var all []raw
	seq := 0
	for _, ln := range strings.Split(strings.ReplaceAll(src, "\r", ""), "\n") {
		ms := tagRe.FindAllStringSubmatchIndex(ln, -1)
		if len(ms) == 0 {
			continue
		}
		// 时间标签必须连续位于行首
		end := 0
		var times []float64
		for _, m := range ms {
			if m[0] != end {
				break
			}
			end = m[1]
			min, _ := strconv.Atoi(ln[m[2]:m[3]])
			sec, _ := strconv.Atoi(ln[m[4]:m[5]])
			frac := 0.0
			if m[6] >= 0 {
				f := ln[m[6]:m[7]]
				v, _ := strconv.Atoi(f)
				frac = float64(v)
				for range len(f) {
					frac /= 10
				}
			}
			times = append(times, float64(min*60+sec)+frac)
		}
		txt := plain(ln[end:])
		if txt == "" {
			continue
		}
		for _, t := range times {
			all = append(all, raw{t, txt, seq})
			seq++
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].t != all[j].t {
			return all[i].t < all[j].t
		}
		return all[i].seq < all[j].seq
	})
	var out []Line
	for i := 0; i < len(all); i++ {
		r := all[i]
		if multiLang && len(out) > 0 && out[len(out)-1].Time == r.t && i > 0 && all[i-1].t == r.t {
			if out[len(out)-1].Trans == "" && r.txt != "" {
				out[len(out)-1].Trans = r.txt
			}
			continue
		}
		out = append(out, Line{Time: r.t, Text: r.txt})
	}
	// 去掉开头的空行
	for len(out) > 0 && out[0].Text == "" && out[0].Trans == "" {
		out = out[1:]
	}
	return out
}

// Index 返回 t 时刻对应的行号（-1 表示第一行之前）。
func Index(lines []Line, t float64) int {
	i := sort.Search(len(lines), func(i int) bool { return lines[i].Time > t+0.05 })
	return i - 1
}
