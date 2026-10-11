package model

import (
	"encoding/json"
	"strings"
	"time"
)

// Resource is a web address with a lifetime, or a File exported by the Goja bridge.
type Resource struct {
	URL           string            `json:"url,omitempty"`
	Path          string            `json:"path,omitempty"`
	Data          string            `json:"data,omitempty"`
	Name          string            `json:"name,omitempty"`
	Type          string            `json:"type,omitempty"`
	EmbeddedCover bool              `json:"embeddedCover,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Expire        float64           `json:"expire,omitempty"` // seconds from receipt; zero means do not index the URL
	ExpiresAt     float64           `json:"expiresAt,omitempty"`
	Provider      string            `json:"provider,omitempty"` // assigned by the host, never trusted from plugin output
}

func (r *Resource) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(s, "embedded:"):
			r.Path, r.EmbeddedCover = strings.TrimPrefix(s, "embedded:"), true
		case strings.HasPrefix(s, "file:"):
			r.Path = strings.TrimPrefix(s, "file:")
		default:
			r.URL = s
		}
		return nil
	}
	type plain Resource
	return json.Unmarshal(b, (*plain)(r))
}

func (r Resource) Empty() bool { return r.URL == "" && r.Path == "" && r.Data == "" }
func (r Resource) Valid() bool {
	return !r.Empty() && (r.URL == "" || r.ExpiresAt > float64(time.Now().UnixMilli())/1000)
}
func (r Resource) Reference() string {
	if r.Path != "" {
		if r.EmbeddedCover {
			return "embedded:" + r.Path
		}
		return "file:" + r.Path
	}
	return r.URL
}

type MusicInfo struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Artist     string          `json:"artist"`
	Album      string          `json:"album"`
	Duration   float64         `json:"duration"`
	Cover      Resource        `json:"cover"`
	LargeCover Resource        `json:"largeCover"`
	Playback   Resource        `json:"playback"`
	Download   Resource        `json:"download"`
	Lyrics     json.RawMessage `json:"lyrics"`
	Format     string          `json:"format"`
	Ext        string          `json:"ext"`
	Extra      map[string]any  `json:"extra,omitempty"`
}
