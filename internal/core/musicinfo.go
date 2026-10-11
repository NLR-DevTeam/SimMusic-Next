package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"simmusic/internal/config"
	"simmusic/internal/model"
	"simmusic/internal/plugin"
	"slices"
	"strings"
	"sync"
	"time"
)

type infoEntry struct {
	Version  string                     `json:"version"`
	Fields   map[string]json.RawMessage `json:"fields"`
	Scenes   map[string]bool            `json:"scenes"`
	Revision uint64                     `json:"revision"`
}
type infoFlight struct {
	done     chan struct{}
	fields   map[string]json.RawMessage
	revision uint64
	err      error
}
type musicIndex struct {
	mu       sync.Mutex
	store    *config.Store
	entries  map[string]*infoEntry
	flights  map[string]*infoFlight
	epochs   map[string]uint64
	revision uint64
}

func (a *App) musicIndex() *musicIndex {
	a.infoOnce.Do(func() {
		x := &musicIndex{store: config.Open(filepath.Join(a.DataDir, "music-index.json"), nil), entries: map[string]*infoEntry{}, flights: map[string]*infoFlight{}, epochs: map[string]uint64{}, revision: uint64(time.Now().UnixMilli())}
		for key := range x.store.All() {
			var e infoEntry
			if x.store.Unmarshal(key, &e) && e.Fields != nil && e.Scenes != nil {
				x.entries[key] = &e
			}
		}
		a.infoIndex = x
	})
	return a.infoIndex
}

// filterIndex removes expired and unbounded web addresses, recursively, while retaining metadata.
func filterIndex(value any, now float64) (any, bool) {
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			return nil, false
		}
	case map[string]any:
		if address, _ := v["url"].(string); address != "" {
			until, _ := v["expiresAt"].(float64)
			if until <= now {
				return nil, false
			}
			return v, true
		}
		out := map[string]any{}
		for key, child := range v {
			if clean, ok := filterIndex(child, now); ok {
				out[key] = clean
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			if clean, ok := filterIndex(child, now); ok {
				out = append(out, clean)
			}
		}
		return out, true
	}
	return value, true
}

// resourceFields may hold a File or a web address instead of plain data.
var resourceFields = []string{"cover", "largeCover", "playback", "download", "lyrics"}

// cachedFields selects what the index keeps: plain data stays until the user clears it,
// web addresses stay for their declared lifetime, and a File is never kept because the
// extension returns it again whenever it is needed.
func cachedFields(fields map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for key, raw := range fields {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		if file, _ := v.(map[string]any); slices.Contains(resourceFields, key) && (file["path"] != nil || file["data"] != nil) {
			continue
		}
		if clean, ok := filterIndex(v, float64(time.Now().UnixMilli())/1000); ok {
			out[key], _ = json.Marshal(clean)
		}
	}
	return out
}

// expireResources is what every invalidation short of the user's own "clear" does to a record: the
// resources (addresses, Files) go and each scene must be asked for again, but plain data such as title,
// artist, album, duration and lyrics text stays, to be replaced only by what a later request returns.
func (e *infoEntry) expireResources(version string) {
	for _, key := range resourceFields {
		if raw := e.Fields[key]; len(raw) > 0 && raw[0] == '{' {
			delete(e.Fields, key)
		}
	}
	e.Scenes = map[string]bool{}
	e.Version = version
}

func mergeFields(base, fresh map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for key, v := range base {
		out[key] = v
	}
	for key, v := range fresh {
		out[key] = v
	}
	return out
}
func decodeInfo(fields map[string]json.RawMessage) model.MusicInfo {
	b, _ := json.Marshal(fields)
	var info model.MusicInfo
	json.Unmarshal(b, &info)
	return info
}
func indexSatisfies(e *infoEntry, scene string) bool {
	if e == nil || !e.Scenes[scene] {
		return false
	}
	for _, key := range []string{"title", "artist", "album", "duration", "cover"} {
		if _, ok := e.Fields[key]; !ok {
			return false
		}
	}
	info := decodeInfo(e.Fields)
	if scene == "play" {
		return info.Playback.Valid()
	}
	if scene == "download" {
		return info.Download.Valid() || info.Playback.Valid()
	}
	return true
}

func (a *App) materialize(p *plugin.Plugin, r model.Resource) (model.Resource, error) {
	if err := p.ValidateResource(r); err != nil {
		return r, err
	}
	if r.Data == "" {
		return r, nil
	}
	b, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		return r, err
	}
	hash := sha256.Sum256(b)
	ext := strings.ToLower(filepath.Ext(filepath.Base(r.Name)))
	if len(ext) > 12 || strings.ContainsAny(ext, `/\:`) {
		ext = ""
	}
	dir := filepath.Join(p.DataDir, "resource-files")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	r.Path = filepath.Join(dir, fmt.Sprintf("%x%s", hash, ext))
	if _, err = os.Stat(r.Path); errors.Is(err, os.ErrNotExist) {
		err = os.WriteFile(r.Path, b, 0o600)
	}
	r.Data = ""
	return r, err
}

func (a *App) normalizeInfo(p *plugin.Plugin, fields map[string]json.RawMessage) error {
	var globalExpire float64
	json.Unmarshal(fields["expire"], &globalExpire)
	for _, key := range resourceFields {
		raw, exists := fields[key]
		if !exists {
			continue
		}
		if key == "lyrics" && len(raw) > 0 && raw[0] == '"' {
			var s string
			json.Unmarshal(raw, &s)
			if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "file:") {
				continue
			}
		}
		var resource model.Resource
		if err := json.Unmarshal(raw, &resource); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		var err error
		resource, err = a.materialize(p, resource)
		if err != nil {
			return err
		}
		resource.Provider = p.ID()
		if resource.URL != "" {
			if resource.Expire <= 0 {
				resource.Expire = globalExpire
			}
			resource.ExpiresAt = 0
			if resource.Expire > 0 {
				resource.ExpiresAt = float64(time.Now().UnixMilli())/1000 + resource.Expire
			}
		}
		fields[key], _ = json.Marshal(resource)
	}
	return nil
}

func (a *App) fetchInfo(s *plugin.Scheme, refs []model.Track, scene string) ([]map[string]json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("协议未注册")
	}
	if s.Has("getMusicInfo") {
		ids := make([]string, len(refs))
		for i, t := range refs {
			ids[i] = t.ID
		}
		var out []map[string]json.RawMessage
		err := s.CallInto(time.Minute, &out, "getMusicInfo", ids, scene)
		if err != nil {
			return nil, err
		}
		p, _ := s.Handler("getMusicInfo")
		for _, fields := range out {
			if err := a.normalizeInfo(p, fields); err != nil {
				return nil, err
			}
		}
		// A separately registered download handler overrides that scene's resource.
		if scene == "download" && s.Has("getDownload") {
			for _, fields := range out {
				var id string
				json.Unmarshal(fields["id"], &id)
				t := model.Track{Source: s.Name, ID: id}
				for _, ref := range refs {
					if ref.ID == id {
						t = ref
						break
					}
				}
				if err := a.fetchDownload(s, t, fields); err != nil {
					return nil, err
				}
			}
		}
		return out, nil
	}
	// Legacy extensions remain supported through the same host cache and scene resolver.
	out := make([]map[string]json.RawMessage, 0, len(refs))
	for _, t := range refs {
		b, _ := json.Marshal(t)
		fields := map[string]json.RawMessage{}
		json.Unmarshal(b, &fields)
		if scene == "play" || scene == "download" {
			var stream model.StreamInfo
			if err := s.CallInto(time.Minute, &stream, "getStream", t); err != nil {
				return nil, err
			}
			fields["playback"], _ = json.Marshal(model.Resource{Path: stream.Path, URL: stream.URL, Headers: stream.Headers})
			fields["format"], _ = json.Marshal(stream.Format)
			var lrc string
			_ = s.CallInto(30*time.Second, &lrc, "getLyrics", t)
			fields["lyrics"], _ = json.Marshal(lrc)
		}
		p := s.Bindings[0].Plugin
		for _, name := range []string{"getStream", "getLyrics"} {
			if provider, _ := s.Handler(name); provider != nil {
				key := "playback"
				if name == "getLyrics" {
					key = "lyrics"
				}
				if raw, ok := fields[key]; ok {
					one := map[string]json.RawMessage{key: raw}
					if err := a.normalizeInfo(provider, one); err != nil {
						return nil, err
					}
					fields[key] = one[key]
				}
			}
		}
		covers := map[string]json.RawMessage{"cover": fields["cover"]}
		if err := a.normalizeInfo(p, covers); err != nil {
			return nil, err
		}
		fields["cover"] = covers["cover"]
		if scene == "download" && s.Has("getDownload") {
			if err := a.fetchDownload(s, t, fields); err != nil {
				return nil, err
			}
		}
		out = append(out, fields)
	}
	return out, nil
}

func (a *App) fetchDownload(s *plugin.Scheme, t model.Track, fields map[string]json.RawMessage) error {
	var raw json.RawMessage
	if err := s.CallInto(time.Minute, &raw, "getDownload", t); err != nil {
		return err
	}
	var down map[string]json.RawMessage
	if len(raw) == 0 {
		return errors.New("下载处理未返回资源")
	}
	if raw[0] != '"' {
		if err := json.Unmarshal(raw, &down); err != nil {
			return err
		}
	}
	resource := raw
	if v := down["download"]; v != nil {
		resource = v
	}
	values := map[string]json.RawMessage{"download": resource}
	for _, key := range []string{"ext", "lyrics", "expire"} {
		if v := down[key]; v != nil {
			values[key] = v
		}
	}
	p, _ := s.Handler("getDownload")
	if err := a.normalizeInfo(p, values); err != nil {
		return err
	}
	for key, v := range values {
		fields[key] = v
	}
	return nil
}

// musicInfos claims missing records before calling the plugin, so overlapping batches share work.
func (a *App) musicInfos(refs []model.Track, scene string) ([]model.MusicInfo, []uint64, error) {
	if scene != "list" && scene != "play" && scene != "download" {
		return nil, nil, errors.New("未知音乐信息场景")
	}
	x := a.musicIndex()
	schemes := map[string]*plugin.Scheme{}
	versions := map[string]string{}
	for _, t := range refs {
		if scene == "download" && !a.CanDownload(t) {
			return nil, nil, errors.New("此音源不支持下载")
		}
		if _, ok := schemes[t.Source]; !ok {
			schemes[t.Source] = a.Plugins.Scheme(t.Source)
			versions[t.Source] = schemes[t.Source].Version()
		}
		if schemes[t.Source] == nil {
			return nil, nil, errors.New("音源插件未启用")
		}
	}
	flights := make(map[string]*infoFlight)
	owned := map[string][]model.Track{}
	epochs := map[string]uint64{}
	x.mu.Lock()
	for _, t := range refs {
		key := t.Key()
		if _, ok := flights[key]; ok {
			continue
		}
		e := x.entries[key]
		if e != nil {
			// Handlers changed: their resources are void, the plain data is still the song's.
			if e.Version != versions[t.Source] {
				e.expireResources(versions[t.Source])
			}
			clean := cachedFields(e.Fields)
			if len(clean) < len(e.Fields) {
				e.Scenes = map[string]bool{}
			}
			e.Fields = clean
		}
		if indexSatisfies(e, scene) {
			f := &infoFlight{done: make(chan struct{}), fields: mergeFields(nil, e.Fields), revision: e.Revision}
			close(f.done)
			flights[key] = f
			continue
		}
		flightKey := fmt.Sprintf("%s\x00%s\x00%d", key, scene, x.epochs[key])
		f := x.flights[flightKey]
		if f == nil {
			f = &infoFlight{done: make(chan struct{})}
			x.flights[flightKey] = f
			owned[t.Source] = append(owned[t.Source], t)
			epochs[key] = x.epochs[key]
		}
		flights[key] = f
	}
	x.mu.Unlock()
	for source, group := range owned {
		s := schemes[source]
		version := versions[source]
		fresh, fetchErr := a.fetchInfo(s, group, scene)
		byID := map[string]map[string]json.RawMessage{}
		if fetchErr == nil {
			for _, fields := range fresh {
				var id string
				json.Unmarshal(fields["id"], &id)
				byID[id] = fields
			}
		}
		x.mu.Lock()
		for _, t := range group {
			key := t.Key()
			f := flights[key]
			f.err = fetchErr
			fields := byID[t.ID]
			if f.err == nil && fields == nil {
				f.err = fmt.Errorf("扩展未返回曲目 %s", t.ID)
			}
			cleared := epochs[key] != x.epochs[key] || version != a.Plugins.Scheme(source).Version()
			if f.err == nil && cleared {
				f.err = errors.New("曲目索引已清理，请重试")
			}
			// A failed refresh, say of an expired cover, takes nothing away: what the index
			// knows about the song is still shown, and the next request tries again.
			if e := x.entries[key]; f.err != nil && !cleared && scene == "list" && e != nil && e.Fields["title"] != nil {
				a.sourceError(t, "歌曲信息刷新失败，沿用索引", f.err)
				f.err, f.fields, f.revision = nil, cachedFields(e.Fields), e.Revision
				delete(x.flights, fmt.Sprintf("%s\x00%s\x00%d", key, scene, epochs[key]))
				close(f.done)
				continue
			}
			if f.err == nil {
				e := x.entries[key]
				if e == nil {
					e = &infoEntry{Version: version, Fields: map[string]json.RawMessage{}, Scenes: map[string]bool{}}
				} else if e.Version != version {
					e.expireResources(version)
				}
				f.fields = mergeFields(cachedFields(e.Fields), fields)
				// A scene may return only its additional fields; complete metadata is supplied by list.
				if scene == "list" {
					for _, field := range []string{"title", "artist", "album", "duration", "cover"} {
						if _, ok := f.fields[field]; !ok {
							f.err = fmt.Errorf("list 音乐信息缺少 %s", field)
							break
						}
					}
				}
				if f.err == nil {
					x.revision++
					f.revision = x.revision
					e.Revision = f.revision
					e.Fields = cachedFields(f.fields)
					e.Scenes[scene] = len(e.Fields) == len(f.fields)
					x.entries[key] = e
					x.store.Set(key, e)
				}
			}
			delete(x.flights, fmt.Sprintf("%s\x00%s\x00%d", key, scene, epochs[key]))
			close(f.done)
		}
		x.mu.Unlock()
	}
	out := make([]model.MusicInfo, len(refs))
	revisions := make([]uint64, len(refs))
	for i, t := range refs {
		f := flights[t.Key()]
		<-f.done
		if f.err != nil {
			return nil, nil, f.err
		}
		out[i], revisions[i] = decodeInfo(f.fields), f.revision
		if out[i].Cover.Empty() && t.Cover != "" {
			json.Unmarshal([]byte(fmt.Sprintf("%q", t.Cover)), &out[i].Cover)
			out[i].Cover.ExpiresAt = t.CoverExpires
			out[i].Cover.Provider = t.CoverProvider
			out[i].Cover.Headers = t.CoverHeaders
		}
		if out[i].LargeCover.Empty() {
			out[i].LargeCover = out[i].Cover
		}
		if out[i].Download.Empty() {
			out[i].Download = out[i].Playback
		}
	}
	return out, revisions, nil
}

func (a *App) GetMusicInfo(refs []model.Track, scene string) ([]model.Track, error) {
	if scene != "list" {
		list, err := a.GetMusicInfo(refs, "list")
		if err != nil {
			return nil, err
		}
		refs = list
	}
	infos, revisions, err := a.musicInfos(refs, scene)
	if err != nil {
		return nil, err
	}
	out := make([]model.Track, len(refs))
	for i, info := range infos {
		out[i] = indexedTrack(refs[i], info, revisions[i])
	}
	a.updateIndexedTracks(out)
	return out, nil
}
func indexedTrack(ref model.Track, info model.MusicInfo, revision uint64) model.Track {
	ref.Title, ref.Artist, ref.Album, ref.Duration = info.Title, info.Artist, info.Album, info.Duration
	ref.Cover, ref.LargeCover = info.Cover.Reference(), info.LargeCover.Reference()
	ref.CoverExpires, ref.LargeCoverExpires = info.Cover.ExpiresAt, info.LargeCover.ExpiresAt
	ref.CoverHeaders, ref.LargeCoverHeaders = info.Cover.Headers, info.LargeCover.Headers
	ref.CoverProvider, ref.LargeCoverProvider = info.Cover.Provider, info.LargeCover.Provider
	ref.InfoReady, ref.InfoRevision, ref.Extra = true, revision, info.Extra
	ref.MetadataReady = true
	return ref
}
func (a *App) resolveMusicInfo(t model.Track, scene string) (model.MusicInfo, model.Track, error) {
	ts, err := a.GetMusicInfo([]model.Track{t}, "list")
	if err != nil {
		return model.MusicInfo{}, t, err
	}
	infos, revisions, err := a.musicInfos(ts, scene)
	if err != nil {
		return model.MusicInfo{}, t, err
	}
	info := infos[0]
	missingDownload := info.Download.Empty()
	missingLyrics := len(info.Lyrics) == 0 || string(info.Lyrics) == "null"
	needLyrics := missingLyrics && (a.Cfg == nil || a.Cfg.String("down.lyricMode") == "embed" || a.Cfg.String("down.lyricMode") == "file")
	if scene == "download" && (missingDownload || needLyrics) {
		play, playRevisions, playErr := a.musicInfos(ts, "play")
		if playErr != nil {
			if missingDownload {
				return info, t, playErr
			}
			a.sourceError(t, "补充下载歌词失败", playErr)
		} else {
			if missingDownload {
				info.Download, info.Playback = play[0].Playback, play[0].Playback
			}
			if missingLyrics {
				info.Lyrics = play[0].Lyrics
			}
			if info.LargeCover.Empty() || info.LargeCover.Reference() == info.Cover.Reference() {
				info.LargeCover = play[0].LargeCover
			}
			if info.Format == "" {
				info.Format = play[0].Format
			}
			revisions[0] = playRevisions[0]
		}
	}
	t = indexedTrack(t, info, revisions[0])
	a.updateIndexedTracks([]model.Track{t})
	return info, t, nil
}

func persistentTracks(ts []model.Track) []model.Track {
	out := append([]model.Track(nil), ts...)
	now := float64(time.Now().UnixMilli()) / 1000
	for i := range out {
		t := &out[i]
		if strings.HasPrefix(t.Cover, "http") && t.CoverExpires <= now {
			t.Cover = ""
		}
		if strings.HasPrefix(t.LargeCover, "http") && t.LargeCoverExpires <= now {
			t.LargeCover = ""
		}
		if clean, ok := filterIndex(t.Extra, now); ok {
			t.Extra, _ = clean.(map[string]any)
		}
		t.InfoReady = false
	}
	return out
}
func (a *App) updateIndexedTracks(ts []model.Track) {
	byKey := map[string]model.Track{}
	for _, t := range ts {
		byKey[t.Key()] = t
	}
	a.mu.Lock()
	for i, t := range a.queue {
		if fresh, ok := byKey[t.Key()]; ok {
			a.queue[i] = fresh
		}
	}
	a.mu.Unlock()
}

func (a *App) ClearMusicInfo(ts []model.Track, scene string) ([]model.Track, error) {
	x := a.musicIndex()
	x.mu.Lock()
	for _, t := range ts {
		key := t.Key()
		x.epochs[key]++
		delete(x.entries, key)
		x.store.Delete(key)
	}
	x.mu.Unlock()
	fresh, err := a.GetMusicInfo(ts, "list")
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	current := ""
	gen := a.loadGen
	if a.index >= 0 && a.index < len(a.queue) {
		current = a.queue[a.index].Key()
	}
	a.mu.Unlock()
	for _, t := range fresh {
		if t.Key() == current {
			if updated, err := a.GetMusicInfo([]model.Track{t}, "play"); err == nil {
				a.loadLyrics(updated[0], gen)
			}
		}
	}
	if scene == "play" {
		return a.GetMusicInfo(fresh, "play")
	}
	return fresh, nil
}

func resourceBytes(p *plugin.Plugin, r model.Resource) ([]byte, error) {
	if err := p.ValidateResource(r); err != nil {
		return nil, err
	}
	if r.Path != "" {
		return os.ReadFile(r.Path)
	}
	if r.Data != "" {
		return base64.StdEncoding.DecodeString(r.Data)
	}
	if r.URL == "" {
		return nil, nil
	}
	req, err := http.NewRequest("GET", r.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 128<<20))
}
func (a *App) musicLyrics(source string, raw json.RawMessage) string {
	text, _ := a.readMusicLyrics(source, raw)
	return text
}

func (a *App) readMusicLyrics(source string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var resource model.Resource
	if json.Unmarshal(raw, &resource) == nil {
		p := a.resourcePlugin(source, resource.Provider)
		if p == nil {
			return "", errors.New("歌词资源提供者未启用")
		}
		b, err := resourceBytes(p, resource)
		return string(b), err
	}
	return "", errors.New("无效歌词资源")
}

func (a *App) resourcePlugin(source, provider string) *plugin.Plugin {
	s := a.Plugins.Scheme(source)
	if s == nil {
		return nil
	}
	if provider == "" {
		return s.Bindings[0].Plugin
	}
	for _, b := range s.Bindings {
		if b.Plugin.ID() == provider && b.Plugin.Running() && (!b.Extension || b.Plugin.HasPermission(plugin.PermSchemeExtend)) {
			return b.Plugin
		}
	}
	return nil
}

func (a *App) FlushMusicIndex() error {
	if a.infoIndex != nil {
		return a.infoIndex.store.Flush()
	}
	return nil
}

// MusicIndexSize reports the size of the persisted index for the settings page.
func (a *App) MusicIndexSize() int64 {
	_ = a.FlushMusicIndex()
	if info, err := os.Stat(filepath.Join(a.DataDir, "music-index.json")); err == nil {
		return info.Size()
	}
	return 0
}

// ClearAllMusicInfo is the user's "清理索引缓存": every record is dropped, plain data included.
func (a *App) ClearAllMusicInfo() { a.resetMusicInfo(nil, true) }

// InvalidateMusicResources follows a change of extensions or their settings: resources are
// requested again, while titles, artists, albums, durations and lyrics text stay in the index.
// A nil match covers every record.
func (a *App) InvalidateMusicResources(match func(key string) bool) { a.resetMusicInfo(match, false) }

func (a *App) resetMusicInfo(match func(key string) bool, drop bool) {
	x := a.musicIndex()
	x.mu.Lock()
	keys := map[string]bool{}
	for key := range x.entries {
		keys[key] = true
	}
	for flight := range x.flights {
		parts := strings.Split(flight, "\x00")
		if len(parts) >= 2 {
			keys[parts[0]+"\x00"+parts[1]] = true
		}
	}
	for key := range keys {
		if match != nil && !match(key) {
			continue
		}
		x.epochs[key]++
		e := x.entries[key]
		if drop || e == nil {
			delete(x.entries, key)
			x.store.Delete(key)
			continue
		}
		e.expireResources(e.Version)
		x.store.Set(key, e)
	}
	x.mu.Unlock()
	a.mu.Lock()
	for i := range a.queue {
		if match == nil || match(a.queue[i].Key()) {
			a.queue[i].InfoReady = false
		}
	}
	var current *model.Track
	if a.index >= 0 && a.index < len(a.queue) && (match == nil || match(a.queue[a.index].Key())) {
		t := a.queue[a.index]
		current = &t
	}
	if current != nil || match == nil {
		a.loadGen++
	}
	gen := a.loadGen
	a.mu.Unlock()
	if current != nil {
		go a.loadLyrics(*current, gen)
	}
}

// cachedLyrics returns the lyrics text the index holds for a song, for when they cannot be requested.
func (a *App) cachedLyrics(t model.Track) string {
	x := a.musicIndex()
	x.mu.Lock()
	defer x.mu.Unlock()
	var text string
	if e := x.entries[t.Key()]; e != nil {
		json.Unmarshal(e.Fields["lyrics"], &text)
	}
	return text
}

func (a *App) cachedTrackRefs(ts []model.Track) []model.Track {
	x := a.musicIndex()
	versions := map[string]string{}
	for _, t := range ts {
		if _, ok := versions[t.Source]; !ok {
			if s := a.Plugins.Scheme(t.Source); s != nil {
				versions[t.Source] = s.Version()
			}
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for i, t := range ts {
		e := x.entries[t.Key()]
		version, registered := versions[t.Source]
		if e == nil || !registered {
			continue
		}
		// After the handlers changed the plain data still shows; the record is just not complete.
		current := e.Version == version
		probe := &infoEntry{Fields: mergeFields(nil, e.Fields), Scenes: e.Scenes}
		if !current {
			probe.expireResources(version)
		}
		fields := cachedFields(probe.Fields)
		if fields["title"] == nil {
			continue
		}
		info := decodeInfo(fields)
		if info.LargeCover.Empty() {
			info.LargeCover = info.Cover
		}
		ts[i] = indexedTrack(t, info, e.Revision)
		ts[i].InfoReady = current && indexSatisfies(&infoEntry{Fields: fields, Scenes: e.Scenes}, "list")
	}
	return ts
}
