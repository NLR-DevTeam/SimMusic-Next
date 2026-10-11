package core

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"simmusic/internal/model"
	"simmusic/internal/winx"
)

// Playlists 管理用户歌单并持久化到 JSON。
type Playlists struct {
	mu    sync.Mutex
	path  string
	lists []model.Playlist
}

func OpenPlaylists(path string) *Playlists {
	p := &Playlists{path: path}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &p.lists)
	}
	return p
}

func (p *Playlists) save() {
	lists := append([]model.Playlist(nil), p.lists...)
	for i := range lists {
		lists[i].Tracks = persistentTracks(lists[i].Tracks)
	}
	b, _ := json.MarshalIndent(lists, "", "  ")
	_ = os.MkdirAll(filepath.Dir(p.path), 0o755)
	_ = os.WriteFile(p.path, b, 0o644)
}

func (p *Playlists) All() []model.Playlist {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]model.Playlist(nil), p.lists...)
}

func (p *Playlists) Get(id string) (model.Playlist, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.lists {
		if l.ID == id {
			return l, true
		}
	}
	return model.Playlist{}, false
}

func (p *Playlists) Create(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	p.lists = append(p.lists, model.Playlist{ID: id, Name: name})
	p.save()
	return id
}

func (p *Playlists) Rename(id, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.lists {
		if p.lists[i].ID == id {
			p.lists[i].Name = name
		}
	}
	p.save()
}

func (p *Playlists) Delete(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.lists {
		if p.lists[i].ID == id {
			p.lists = append(p.lists[:i], p.lists[i+1:]...)
			break
		}
	}
	p.save()
}

// Add 添加曲目（跳过重复），返回实际新增数量。
func (p *Playlists) Add(id string, ts ...model.Track) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for i := range p.lists {
		if p.lists[i].ID != id {
			continue
		}
		exist := map[string]bool{}
		for _, t := range p.lists[i].Tracks {
			exist[t.Key()] = true
		}
		for _, t := range ts {
			if !exist[t.Key()] {
				p.lists[i].Tracks = append(p.lists[i].Tracks, t)
				exist[t.Key()] = true
				n++
			}
		}
	}
	p.save()
	return n
}

func (p *Playlists) Remove(id string, key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.lists {
		if p.lists[i].ID != id {
			continue
		}
		ts := p.lists[i].Tracks[:0]
		for _, t := range p.lists[i].Tracks {
			if t.Key() != key {
				ts = append(ts, t)
			}
		}
		p.lists[i].Tracks = ts
	}
	p.save()
}

// Move 把歌单中的第 from 首移到第 to 位。
func (p *Playlists) Move(id string, from, to int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.lists {
		if n := len(p.lists[i].Tracks); p.lists[i].ID == id && from >= 0 && to >= 0 && from < n && to < n && from != to {
			p.lists[i].Tracks = moveItem(p.lists[i].Tracks, from, to)
			p.save()
		}
	}
}

// ---------- 系统操作 ----------

func (a *App) doSystemAction(act string) {
	a.SavePosition()
	switch act {
	case "sleep":
		if err := winx.Suspend(); err != nil {
			a.fail("休眠失败", err)
		}
	case "shutdown":
		if err := winx.Shutdown(); err != nil {
			a.fail("关机失败", err)
		}
	}
}

func httpGetBytes(url string) []byte {
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	return b
}
