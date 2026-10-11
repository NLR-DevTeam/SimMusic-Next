package plugin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var pluginID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validateManifest(man Manifest) error {
	if !pluginID.MatchString(man.ID) || strings.HasSuffix(man.ID, ".") {
		return errors.New("plugin.json 的 id 必须是安全的扩展标识")
	}
	reserved := strings.ToUpper(strings.Split(man.ID, ".")[0])
	if reserved == "CON" || reserved == "PRN" || reserved == "AUX" || reserved == "NUL" || regexp.MustCompile(`^(COM|LPT)[0-9]$`).MatchString(reserved) {
		return errors.New("扩展 id 不能是 Windows 保留名称")
	}
	if man.Main != "" && !safeArchivePath(man.Main) {
		return errors.New("扩展入口必须是包内相对路径")
	}
	return nil
}

func safeArchivePath(name string) bool {
	return name != "" && !strings.ContainsAny(name, `\:`) && !strings.HasPrefix(name, "/") && path.Clean(name) == strings.TrimSuffix(name, "/") && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

// PreparePackage extracts into a temporary directory before any code or grants run.
func PreparePackage(reader io.Reader) (dir string, manifest Manifest, err error) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
	if err != nil {
		return "", manifest, err
	}
	if len(data) > 64<<20 {
		return "", manifest, errors.New("扩展包超过 64 MB")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", manifest, errors.New("无效的 .simex ZIP 扩展包")
	}
	if len(archive.File) > 4096 {
		return "", manifest, errors.New("扩展包文件过多")
	}
	dir, err = os.MkdirTemp("", "simmusic-simex-")
	if err != nil {
		return "", manifest, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	seen := map[string]bool{}
	var total uint64
	for _, file := range archive.File {
		name := strings.TrimSuffix(file.Name, "/")
		key := strings.ToLower(name)
		if !safeArchivePath(name) || seen[key] || file.Mode()&os.ModeSymlink != 0 {
			return dir, manifest, fmt.Errorf("不安全或重复的包内路径：%s", file.Name)
		}
		for _, part := range strings.Split(name, "/") {
			if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
				return dir, manifest, errors.New("包内路径含无效 Windows 文件名")
			}
		}
		seen[key] = true
		if file.UncompressedSize64 > 128<<20 {
			return dir, manifest, errors.New("扩展文件过大")
		}
		total += file.UncompressedSize64
		if total > 128<<20 {
			return dir, manifest, errors.New("扩展包解压后超过 128 MB")
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if file.FileInfo().IsDir() {
			err = os.MkdirAll(target, 0o755)
		} else {
			err = os.MkdirAll(filepath.Dir(target), 0o755)
			if err == nil {
				var source io.ReadCloser
				source, err = file.Open()
				if err == nil {
					var output *os.File
					output, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
					if err == nil {
						_, err = io.Copy(output, io.LimitReader(source, int64(file.UncompressedSize64)+1))
						closeErr := output.Close()
						if err == nil {
							err = closeErr
						}
					}
					source.Close()
				}
			}
		}
		if err != nil {
			return dir, manifest, err
		}
	}
	manifest, err = ReadManifest(dir)
	if err != nil {
		return dir, manifest, err
	}
	if info, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(manifest.Main))); statErr != nil || !info.Mode().IsRegular() {
		return dir, manifest, errors.New("扩展包缺少有效的入口脚本")
	}
	return dir, manifest, nil
}

func (m *Manager) setDevelopment(id, dir string) {
	entries := map[string]string{}
	m.enabled.Unmarshal("plugin.development", &entries)
	if dir == "" {
		delete(entries, id)
	} else {
		entries[id] = dir
	}
	m.enabled.Set("plugin.development", entries)
}

// LoadDevelopment references the original folder; removal never deletes it.
func (m *Manager) LoadDevelopment(dir string, denied ...string) (*Plugin, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	man, err := ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(man.Main))); err != nil {
		return nil, err
	}
	if old := m.Get(man.ID); old != nil {
		if old.Builtin {
			return nil, errors.New("不能覆盖内置插件")
		}
		if !old.Development && strings.EqualFold(old.Dir, dir) {
			return nil, errors.New("请选择程序扩展目录以外的开发文件夹")
		}
		if err := m.Remove(man.ID); err != nil {
			return nil, err
		}
	}
	m.GrantAll(man, denied...)
	m.loading = true
	err = m.add(os.DirFS(dir), dir, false)
	m.loading = false
	if err != nil {
		return nil, err
	}
	m.Get(man.ID).Development = true
	m.setDevelopment(man.ID, dir)
	m.RestartOrdered()
	return m.Get(man.ID), nil
}

func SameManifest(a, b Manifest) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
