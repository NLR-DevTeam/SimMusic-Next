package plugin

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/dop251/goja"
	"net/url"
	"os"
	"path/filepath"
	"simmusic/internal/audio"
	"simmusic/internal/model"
	"strings"
)

func (p *Plugin) installFile() {
	p.rt.Set("File", func(call goja.ConstructorCall) *goja.Object {
		parts := call.Argument(0).ToObject(p.rt)
		var data []byte
		for i, n := 0, int(parts.Get("length").ToInteger()); i < n; i++ {
			value := parts.Get(fmt.Sprint(i))
			var chunk []byte
			switch v := value.Export().(type) {
			case string:
				chunk = []byte(v)
			case goja.ArrayBuffer:
				chunk = v.Bytes()
			case []uint8:
				chunk = v
			default:
				view := value.ToObject(p.rt)
				if bufferValue := view.Get("buffer"); bufferValue != nil {
					if buffer, ok := bufferValue.Export().(goja.ArrayBuffer); ok {
						offset, length := int(view.Get("byteOffset").ToInteger()), int(view.Get("byteLength").ToInteger())
						chunk = buffer.Bytes()[offset : offset+length]
						break
					}
				}
				var resource model.Resource
				if err := p.rt.ExportTo(value, &resource); err != nil {
					p.throw(errors.New("File part 必须是文本、ArrayBuffer、TypedArray 或 File"))
				}
				var err error
				chunk, err = p.fileBytes(resource)
				if err != nil {
					p.throw(err)
				}
			}
			if len(data)+len(chunk) > 128<<20 {
				p.throw(errors.New("File 超过 128 MiB；大文件请使用 sim.fs.file(path)"))
			}
			data = append(data, chunk...)
		}
		typ := ""
		if options := call.Argument(2); !goja.IsUndefined(options) && !goja.IsNull(options) {
			if v := options.ToObject(p.rt).Get("type"); v != nil && !goja.IsUndefined(v) {
				typ = v.String()
			}
		}
		call.This.Set("data", base64.StdEncoding.EncodeToString(data))
		call.This.Set("name", call.Argument(1).String())
		call.This.Set("type", typ)
		call.This.Set("size", len(data))
		return call.This
	})
	p.rt.Set("__fileRead", func(value goja.Value, text bool) goja.Value {
		var resource model.Resource
		if err := p.rt.ExportTo(value, &resource); err != nil {
			p.throw(err)
		}
		return p.async(func() (any, error) {
			b, err := p.fileBytes(resource)
			if err != nil {
				return nil, err
			}
			if text {
				return string(b), nil
			}
			return jsBuilder(func(rt *goja.Runtime) goja.Value { return rt.ToValue(rt.NewArrayBuffer(b)) }), nil
		})
	})
	_, err := p.rt.RunString(`Object.defineProperties(File.prototype, {
  text: {value() { return __fileRead(this, true) }},
  arrayBuffer: {value() { return __fileRead(this, false) }}
 })`)
	if err != nil {
		p.throw(err)
	}
}

func (p *Plugin) pathFile(path string, cover bool) *goja.Object {
	path = p.checkPath(path)
	info, err := os.Stat(path)
	if err != nil {
		p.throw(err)
	}
	o := p.rt.NewObject()
	o.SetPrototype(p.rt.Get("File").ToObject(p.rt).Get("prototype").ToObject(p.rt))
	o.Set("path", path)
	o.Set("name", filepath.Base(path))
	o.Set("size", info.Size())
	o.Set("lastModified", info.ModTime().UnixMilli())
	if cover {
		o.Set("embeddedCover", true)
	}
	return o
}

func (p *Plugin) fileBytes(r model.Resource) ([]byte, error) {
	if err := p.ValidateResource(r); err != nil {
		return nil, err
	}
	if r.Path != "" {
		if r.EmbeddedCover {
			return audio.ReadCover(r.Path), nil
		}
		return os.ReadFile(r.Path)
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// ValidateResource runs outside the VM too; never throws into Goja from a host goroutine.
func (p *Plugin) ValidateResource(r model.Resource) error {
	if r.Path != "" {
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			return err
		}
		base, _ := filepath.Abs(p.DataDir)
		rel, err := filepath.Rel(base, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if !p.HasPermission(PermFSRead) {
				return p.permError(PermFSRead)
			}
		}
	}
	if r.URL != "" {
		if !p.HasPermission(PermNetwork) {
			return p.permError(PermNetwork)
		}
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return errors.New("资源必须返回 File 或 HTTP(S) 地址")
		}
		if len(p.Manifest.Hosts) != 0 {
			for _, h := range p.Manifest.Hosts {
				h = strings.ToLower(h)
				host := strings.ToLower(u.Hostname())
				if host == h || strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:]) {
					return nil
				}
			}
			return errors.New("资源域名不在扩展 hosts 声明中")
		}
	}
	return nil
}
