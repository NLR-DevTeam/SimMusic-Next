// Package config 提供带默认值、自动持久化的键值配置存储。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	mu        sync.RWMutex
	flushMu   sync.Mutex
	path      string
	data      map[string]any
	defaults  map[string]any
	saveTimer *time.Timer
	listeners []func(key string, value any)
}

// Open 从 path 读取配置（不存在则为空），defaults 提供缺省值。
func Open(path string, defaults map[string]any) *Store {
	s := &Store{path: path, data: map[string]any{}, defaults: defaults}
	if s.defaults == nil {
		s.defaults = map[string]any{}
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s
}

func (s *Store) Get(key string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.data[key]; ok {
		return v
	}
	return s.defaults[key]
}

func (s *Store) Bool(key string) bool {
	v, _ := s.Get(key).(bool)
	return v
}

func (s *Store) Float(key string) float64 {
	switch v := s.Get(key).(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func (s *Store) Int(key string) int { return int(s.Float(key)) }

func (s *Store) String(key string) string {
	v, _ := s.Get(key).(string)
	return v
}

// Unmarshal 将 key 对应的值解码到 out 中（用于复杂结构）。
func (s *Store) Unmarshal(key string, out any) bool {
	v := s.Get(key)
	if v == nil {
		return false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

func (s *Store) Set(key string, value any) {
	// 统一转换为 JSON 兼容的类型，保证 Get 读取一致。
	if b, err := json.Marshal(value); err == nil {
		var norm any
		if json.Unmarshal(b, &norm) == nil {
			value = norm
		}
	}
	s.mu.Lock()
	s.data[key] = value
	ls := append([]func(string, any){}, s.listeners...)
	s.scheduleSave()
	s.mu.Unlock()
	for _, l := range ls {
		l(key, value)
	}
}

func (s *Store) Delete(key string) {
	s.mu.Lock()
	delete(s.data, key)
	s.scheduleSave()
	s.mu.Unlock()
}

// All 返回当前所有显式设置的值与默认值的合并结果。
func (s *Store) All() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.data)+len(s.defaults))
	for k, v := range s.defaults {
		out[k] = v
	}
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

func (s *Store) SetDefault(key string, v any) {
	s.mu.Lock()
	s.defaults[key] = v
	s.mu.Unlock()
}

func (s *Store) OnChange(f func(key string, value any)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, f)
	s.mu.Unlock()
}

func (s *Store) scheduleSave() {
	if s.saveTimer != nil {
		s.saveTimer.Stop()
	}
	s.saveTimer = time.AfterFunc(300*time.Millisecond, func() { _ = s.Flush() })
}

// Flush 立即写盘。
func (s *Store) Flush() error {
	// Serialize timer/manual flushes before taking the snapshot. Otherwise an
	// older write can replace newer index data or race on the shared .tmp file.
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.RLock()
	b, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
