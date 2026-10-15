package core

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"simmusic/internal/model"
)

// 拖入文件由主程序直接处理，这是唯一一处越过扩展操作「本地音乐」数据的地方：
// 文件夹导入为本地目录；音频文件加入正在查看的歌单，或复制进正在查看的本地目录。

const localPlugin = "local"

func (a *App) localFolders() []string {
	var folders []string
	if p := a.Plugins.Get(localPlugin); p != nil {
		p.Settings.Unmarshal("folders", &folders)
	}
	return folders
}

// localExts 返回「本地音乐」设置中的音频扩展名。
func (a *App) localExts() []string {
	text := ".mp3 .wav .flac .ogg"
	if p := a.Plugins.Get(localPlugin); p != nil {
		if value, ok := p.Settings.Get("exts").(string); ok && value != "" {
			text = value
		}
	}
	var exts []string
	for _, ext := range strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '\t' }) {
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		exts = append(exts, strings.ToLower(ext))
	}
	return exts
}

// DropFiles handles paths dropped on the window while the page described by kind / pluginID / id is shown.
// It returns every .simex among them, in order, for the installer, and whether the page shown has changed.
func (a *App) DropFiles(paths []string, kind, pluginID, id string) (simex []string, refresh bool) {
	local := a.Plugins.Get(localPlugin)
	running := local != nil && local.Running()
	exts := a.localExts()
	var folders, audio []string
	for _, path := range paths {
		info, err := os.Stat(path)
		switch {
		case err != nil:
		case info.IsDir():
			folders = append(folders, path)
		case strings.EqualFold(filepath.Ext(path), ".simex"):
			simex = append(simex, path)
		case slices.Contains(exts, strings.ToLower(filepath.Ext(path))):
			audio = append(audio, path)
		}
	}
	if running && len(folders) > 0 {
		list := a.localFolders()
		added := ""
		for _, folder := range folders {
			if !slices.ContainsFunc(list, func(item string) bool { return strings.EqualFold(item, folder) }) {
				list = append(list, folder)
				added = folder
			}
		}
		if added == "" {
			a.Toast("目录已存在")
		} else {
			local.Settings.Set("folders", list)
			a.Toast("已添加文件夹")
			a.OpenSidebar(localPlugin, added)
		}
	}
	if !running || len(audio) == 0 {
		return simex, false
	}
	switch {
	case kind == "playlist":
		var tracks []model.Track
		if err := local.CallInto(time.Minute, &tracks, "openFiles", audio); err != nil {
			a.fail("操作失败", err)
			return simex, false
		}
		tracks, err := a.pluginTracks(local, tracks)
		if err != nil {
			a.fail("操作失败", err)
			return simex, false
		}
		if count := a.Lists.Add(id, tracks...); count > 0 {
			a.Toast(fmt.Sprintf("已添加 %d 首曲目", count))
		} else {
			a.Toast("歌曲已在歌单中")
		}
		return simex, true
	case kind == "menu" && pluginID == localPlugin && slices.Contains(a.localFolders(), id):
		count := 0
		for _, path := range audio {
			target := filepath.Join(id, filepath.Base(path))
			if _, err := os.Stat(target); err == nil {
				continue // already there, or a file of the same name: never overwrite
			}
			if copyFile(path, target) == nil {
				count++
			}
		}
		if count > 0 {
			a.Toast(fmt.Sprintf("已导入 %d 个文件", count))
		} else {
			a.Toast("文件已在此目录中")
		}
		return simex, count > 0
	}
	return simex, false
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(target)
		return err
	}
	return out.Close()
}
