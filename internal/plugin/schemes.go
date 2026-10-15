package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"simmusic/internal/model"
)

// SchemeDeclaration registers a protocol, or patches an existing protocol when
// listed in schemeExtensions. Handler values name CommonJS exports in this VM.
type SchemeDeclaration struct {
	Scheme     string            `json:"scheme"`
	Handlers   map[string]string `json:"handlers,omitempty"`
	Download   *bool             `json:"download,omitempty"`
	TrackMenus []model.Action    `json:"trackMenus,omitempty"`
}

type SchemeBinding struct {
	Plugin      *Plugin
	Declaration SchemeDeclaration
	Extension   bool
}

type Scheme struct {
	Name     string
	Bindings []SchemeBinding
}

var schemeName = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)
var schemeFunctions = []string{"getMusicInfo", "getDownload", "getTrackActions", "onTrackAction"}

func (b SchemeBinding) MenuPrefix() string {
	return "scheme:" + b.Declaration.Scheme + "/"
}

func (b SchemeBinding) Menus() []model.Action {
	return append(append([]model.Action(nil), b.Plugin.Manifest.TrackMenus...), b.Declaration.TrackMenus...)
}

func (s *Scheme) Handler(name string) (*Plugin, string) {
	if s == nil {
		return nil, ""
	}
	for i := len(s.Bindings) - 1; i >= 0; i-- {
		b := s.Bindings[i]
		if b.Extension && !b.Plugin.HasPermission(PermSchemeExtend) {
			continue
		}
		if fn := b.Declaration.Handlers[name]; fn != "" && b.Plugin.Running() {
			return b.Plugin, fn
		}
	}
	return nil, ""
}

func (s *Scheme) Has(name string) bool { p, _ := s.Handler(name); return p != nil }

func (s *Scheme) CallInto(timeout time.Duration, out any, name string, args ...any) error {
	p, fn := s.Handler(name)
	if p == nil {
		return ErrNotImplemented
	}
	// Named handlers use the same permission checks as the standard exports.
	if (name == "getDownload" || name == "getMusicInfo" && len(args) > 1 && args[1] == "download") && !s.DownloadAllowed() {
		return errors.New("协议不支持下载或下载菜单未获授权")
	}
	if (name == "getTrackActions" || name == "onTrackAction") && !p.HasPermission(PermTrackMenu) {
		return p.permError(PermTrackMenu)
	}
	args = append(args, s.Name)
	v, err := p.call(timeout, fn, args...)
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (s *Scheme) downloadBinding() (SchemeBinding, bool) {
	if s == nil {
		return SchemeBinding{}, false
	}
	for i := len(s.Bindings) - 1; i >= 0; i-- {
		b := s.Bindings[i]
		if b.Extension && !b.Plugin.HasPermission(PermSchemeExtend) {
			continue
		}
		if b.Declaration.Download != nil {
			return b, *b.Declaration.Download
		}
		if !b.Extension {
			return b, false
		}
	}
	return SchemeBinding{}, false
}

func (s *Scheme) DownloadAllowed() bool {
	b, on := s.downloadBinding()
	if !on || !s.Has("getMusicInfo") && !s.Has("getDownload") {
		return false
	}
	return b.Plugin.TrackMenuGranted(b.MenuPrefix() + "download")
}

func (s *Scheme) Version() string {
	if s == nil {
		return ""
	}
	var parts []string
	for _, b := range s.Bindings {
		if b.Extension && !b.Plugin.HasPermission(PermSchemeExtend) {
			continue
		}
		data, _ := json.Marshal(b.Declaration)
		parts = append(parts, b.Plugin.ID()+":"+b.Plugin.Manifest.Version+":"+string(data))
	}
	return strings.Join(parts, "|")
}

func (m *Manager) Scheme(name string) *Scheme {
	m.schemeMu.RLock()
	defer m.schemeMu.RUnlock()
	s := m.schemes[name]
	if s == nil {
		return nil
	}
	return &Scheme{Name: s.Name, Bindings: append([]SchemeBinding(nil), s.Bindings...)}
}

func (m *Manager) Schemes() map[string]any {
	out := map[string]any{}
	m.schemeMu.RLock()
	names := make([]string, 0, len(m.schemes))
	for name := range m.schemes {
		names = append(names, name)
	}
	m.schemeMu.RUnlock()
	for _, name := range names {
		s := m.Scheme(name)
		if s != nil {
			providers := make([]string, 0, len(s.Bindings))
			for _, b := range s.Bindings {
				if !b.Extension || b.Plugin.HasPermission(PermSchemeExtend) {
					providers = append(providers, b.Plugin.ID())
				}
			}
			out[name] = map[string]any{"plugin": s.Bindings[0].Plugin.ID(), "providers": providers, "canDownload": s.DownloadAllowed(), "version": s.Version()}
		}
	}
	return out
}

func (m *Manager) SourcesFor(id string) map[string]bool {
	out := map[string]bool{}
	m.schemeMu.RLock()
	defer m.schemeMu.RUnlock()
	for name, s := range m.schemes {
		for _, b := range s.Bindings {
			if b.Plugin.ID() == id {
				out[name] = true
				break
			}
		}
	}
	return out
}

// Registration is transactional: one bad or duplicate declaration leaves no
// partial protocols or patches behind, and the extension is marked failed.
func (m *Manager) registerSchemes(p *Plugin) error {
	owned := p.Manifest.Schemes
	m.schemeMu.Lock()
	defer m.schemeMu.Unlock()
	pending := map[string]*Scheme{}
	get := func(name string) *Scheme {
		if s := pending[name]; s != nil {
			return s
		}
		return m.schemes[name]
	}
	add := func(d SchemeDeclaration, extension bool) error {
		if !schemeName.MatchString(d.Scheme) {
			return fmt.Errorf("无效 scheme：%s", d.Scheme)
		}
		s := get(d.Scheme)
		if !extension && s != nil {
			return fmt.Errorf("scheme %s 已被扩展 %s 注册", d.Scheme, s.Bindings[0].Plugin.ID())
		}
		if extension && s == nil {
			return fmt.Errorf("不能追加不存在的 scheme：%s", d.Scheme)
		}
		handlers := map[string]string{}
		for key, value := range d.Handlers {
			handlers[key] = value
		}
		d.Handlers = handlers
		if !extension {
			for _, name := range schemeFunctions {
				if _, ok := d.Handlers[name]; !ok && p.Has(name) {
					d.Handlers[name] = name
				}
			}
			if d.Download == nil {
				d.Download = p.Manifest.Capabilities.Download
			}
		}
		for name, fn := range d.Handlers {
			valid := false
			for _, known := range schemeFunctions {
				if name == known {
					valid = true
				}
			}
			if !valid || fn == "" || !p.Has(fn) {
				return fmt.Errorf("scheme %s 的处理函数 %s → %s 不存在或不受支持", d.Scheme, name, fn)
			}
		}
		if s == nil {
			s = &Scheme{Name: d.Scheme}
		} else {
			s = &Scheme{Name: s.Name, Bindings: append([]SchemeBinding(nil), s.Bindings...)}
		}
		s.Bindings = append(s.Bindings, SchemeBinding{Plugin: p, Declaration: d, Extension: extension})
		pending[d.Scheme] = s
		return nil
	}
	for _, d := range owned {
		if err := add(d, false); err != nil {
			return err
		}
	}
	if len(p.Manifest.SchemeExtensions) > 0 && p.HasPermission(PermSchemeExtend) {
		for _, d := range p.Manifest.SchemeExtensions {
			if err := add(d, true); err != nil {
				return err
			}
		}
	} else if len(p.Manifest.SchemeExtensions) > 0 {
		p.logf("追加协议支持未获授权，未注册 schemeExtensions")
	}
	for name, s := range pending {
		m.schemes[name] = s
	}
	return nil
}

func (m *Manager) unregisterSchemes(p *Plugin) {
	m.schemeMu.Lock()
	defer m.schemeMu.Unlock()
	for name, s := range m.schemes {
		if s.Bindings[0].Plugin == p {
			delete(m.schemes, name)
			continue
		}
		kept := s.Bindings[:0]
		for _, b := range s.Bindings {
			if b.Plugin != p {
				kept = append(kept, b)
			}
		}
		s.Bindings = kept
	}
}

func (m *Manager) ordered() {
	var ids []string
	m.enabled.Unmarshal("plugin.loadOrder", &ids)
	rank := map[string]int{}
	for i, id := range ids {
		rank[id] = i + 1
	}
	m.mu.Lock()
	sort.SliceStable(m.plugins, func(i, j int) bool {
		a, b := rank[m.plugins[i].ID()], rank[m.plugins[j].ID()]
		return a != 0 && (b == 0 || a < b)
	})
	m.mu.Unlock()
}

func (m *Manager) RestartOrdered() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	for _, p := range m.All() {
		if m.restartable(p) {
			p.stop()
		}
	}
	m.ordered()
	for _, p := range m.All() {
		if !m.IsEnabled(p.ID()) || !m.restartable(p) {
			continue
		}
		err := p.start()
		p.setErr(err)
		if err != nil {
			p.logf("加载失败：%v", err)
		}
	}
}

func (m *Manager) SetOrder(ids []string) error {
	if len(ids) != len(m.All()) {
		return errors.New("扩展加载顺序必须包含全部扩展")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || m.Get(id) == nil {
			return errors.New("扩展加载顺序包含重复或未知扩展")
		}
		seen[id] = true
	}
	m.enabled.Set("plugin.loadOrder", ids)
	if err := m.enabled.Flush(); err != nil {
		return err
	}
	m.RestartOrdered()
	return nil
}

// ValidateTracks keeps Source as the protocol name; an extension that registers exactly one protocol
// may leave it out.
func (m *Manager) ValidateTracks(p *Plugin, tracks []model.Track) error {
	defaultName := ""
	if len(p.Manifest.Schemes) == 1 {
		defaultName = p.Manifest.Schemes[0].Scheme
	}
	for i := range tracks {
		if tracks[i].Source == "" {
			tracks[i].Source = defaultName
		}
		s := m.Scheme(tracks[i].Source)
		allowed := false
		if s != nil {
			for _, b := range s.Bindings {
				if b.Plugin == p && (!b.Extension || p.HasPermission(PermSchemeExtend)) {
					allowed = true
				}
			}
		}
		if !allowed {
			return fmt.Errorf("扩展 %s 未注册或获准追加 scheme %s", p.ID(), tracks[i].Source)
		}
		tracks[i].CoverProvider = ""
		tracks[i].LargeCoverProvider = ""
	}
	return nil
}
