package webui

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

func clearRuntimeCache(dataDir, name string) error {
	if name != "cef-cache" && name != "cef-cache-106" {
		return errors.New("无效运行时缓存目录")
	}
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	target := filepath.Join(root, name)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel != name {
		return errors.New("缓存目录不在应用数据目录内")
	}
	// The browser storage of extension webviews (cookies, sign-ins) lives in cef-cache/webview
	// and is kept; everything else is the cache of the interface itself.
	remove := func() error {
		entries, err := os.ReadDir(target)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		kept := false
		for _, entry := range entries {
			if name == "cef-cache" && entry.Name() == "webview" && entry.IsDir() {
				kept = true
				continue
			}
			if err := os.RemoveAll(filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		if kept {
			return nil
		}
		return os.Remove(target)
	}
	// CEF subprocesses can briefly retain file handles after the browser host exits.
	for attempt := 0; attempt < 10; attempt++ {
		if err = remove(); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}
