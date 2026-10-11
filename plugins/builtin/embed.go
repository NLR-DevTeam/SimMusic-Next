// Package builtin supplies first-run files; extensions execute from editable data/plugins directories.
package builtin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed local
var files embed.FS

// Local 返回内置「本地音乐」插件的文件系统。
func Local() fs.FS {
	sub, _ := fs.Sub(files, "local")
	return sub
}

// DeployLocal updates only unmodified bundle files, retaining user edits across upgrades.
func DeployLocal(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "plugins", "local")
	stamp := filepath.Join(dir, ".bundle-hashes.json")
	previous := map[string]string{}
	if b, err := os.ReadFile(stamp); err == nil {
		json.Unmarshal(b, &previous)
	}
	next := map[string]string{}
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	err := fs.WalkDir(Local(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(dir, path), 0o755)
		}
		bundled, err := fs.ReadFile(Local(), path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		current, readErr := os.ReadFile(target)
		if os.IsNotExist(readErr) || readErr == nil && digest(current) == previous[path] {
			if err := os.WriteFile(target, bundled, 0o644); err != nil {
				return err
			}
		} else if readErr != nil {
			return readErr
		}
		next[path] = digest(bundled)
		return nil
	})
	if err != nil {
		return dir, err
	}
	// 旧版本地扩展自行保存的索引；索引缓存现由主程序统一管理。
	_ = os.Remove(filepath.Join(dataDir, "plugin-data", "local", "index.json"))
	b, _ := json.MarshalIndent(next, "", "  ")
	return dir, os.WriteFile(stamp, b, 0o644)
}
