package core

import (
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"simmusic/internal/model"
	"simmusic/internal/plugin"
)

// Search 把用户输入的原搜索词与界面分出的词块一并交给扩展，由扩展决定怎么用。
// 指定音源时结果保持该扩展给出的顺序；搜索全部音源时，待各扩展都返回后由主程序按相关度排序。
func (a *App) Search(keyword string, words []string, source string) ([]model.Track, error) {
	if source != "" {
		p := a.Plugins.Get(source)
		if p == nil || !p.Running() {
			return nil, errors.New("音源扩展未启用")
		}
		return a.searchPlugin(p, keyword, words)
	}
	var plugins []*plugin.Plugin
	for _, p := range a.Plugins.Running() {
		if p.Has("search") {
			plugins = append(plugins, p)
		}
	}
	results := make([][]model.Track, len(plugins))
	var wg sync.WaitGroup
	for i, p := range plugins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One failing source (say, an unconfigured one) must not hide the others' results.
			ts, err := a.searchPlugin(p, keyword, words)
			if err != nil {
				log.Printf("search %s: %v", p.ID(), err)
				return
			}
			results[i] = a.searchMetadata(ts)
		}()
	}
	wg.Wait()
	type ranked struct {
		track              model.Track
		score, rank, order int
	}
	var all []ranked
	for order, ts := range results {
		for rank, t := range ts {
			all = append(all, ranked{t, relevance(t, keyword, words), rank, order})
		}
	}
	// Equally relevant songs keep each source's own ranking, interleaved across sources.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return all[i].order < all[j].order
	})
	out := make([]model.Track, len(all))
	for i, item := range all {
		out[i] = item.track
	}
	return out, nil
}

func (a *App) searchPlugin(p *plugin.Plugin, keyword string, words []string) ([]model.Track, error) {
	var ts []model.Track
	if err := p.CallInto(30*time.Second, &ts, "search", keyword, words); err != nil {
		return nil, err
	}
	return a.pluginTracks(p, ts)
}

// searchMetadata resolves the references that carry no title yet, so they can be ranked.
func (a *App) searchMetadata(ts []model.Track) []model.Track {
	var missing []model.Track
	for _, t := range ts {
		if !t.MetadataReady && !t.InfoReady {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return ts
	}
	fresh, err := a.GetMusicInfo(missing, "list")
	if err != nil {
		return ts
	}
	known := map[string]model.Track{}
	for _, t := range fresh {
		known[t.Key()] = t
	}
	for i, t := range ts {
		if f, ok := known[t.Key()]; ok {
			ts[i] = f
		}
	}
	return ts
}

var commonWords = map[string]bool{"的": true, "大": true, "小": true, "了": true, "the": true, "a": true}

// relevance counts the matched words, a title match weighing more than artist or album,
// and prefers titles that are, or contain, the whole search text.
func relevance(t model.Track, keyword string, words []string) int {
	title, artist, album := strings.ToLower(t.Title), strings.ToLower(t.Artist), strings.ToLower(t.Album)
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	score := 0
	switch {
	case keyword == "":
	case title == keyword:
		score += 8
	case strings.Contains(title, keyword):
		score += 4
	}
	for _, word := range words {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" || commonWords[word] {
			continue
		}
		switch {
		case strings.Contains(title, word):
			score += 3
		case strings.Contains(artist, word):
			score += 2
		case strings.Contains(album, word):
			score++
		}
	}
	return score
}
