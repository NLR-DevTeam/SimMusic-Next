package core

import (
	"os"
	"path/filepath"
	"testing"

	"simmusic/internal/audio"
	"simmusic/internal/config"
	"simmusic/internal/plugin"
	"simmusic/plugins/builtin"
)

func TestDropFiles(t *testing.T) {
	dir := t.TempDir()
	a := &App{DataDir: dir, Cfg: config.Open(filepath.Join(dir, "config.json"), Defaults), State: config.Open(filepath.Join(dir, "state.json"), nil), Engine: &audio.Engine{}, index: -1}
	a.Plugins = plugin.NewManager(filepath.Join(dir, "plugin-data"), filepath.Join(dir, "plugins"), a.State, a)
	a.Lists = OpenPlaylists(filepath.Join(dir, "playlists.json"))
	if err := a.Plugins.AddBuiltin(builtin.Local()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Plugins.StopAll)
	library, outside := filepath.Join(dir, "library"), filepath.Join(dir, "outside")
	os.MkdirAll(library, 0o755)
	os.MkdirAll(outside, 0o755)
	song, text, pack := filepath.Join(outside, "song.mp3"), filepath.Join(outside, "notes.txt"), filepath.Join(outside, "x.simex")
	for _, file := range []string{song, text, pack} {
		os.WriteFile(file, []byte("data"), 0o644)
	}

	// A folder becomes a local directory, once; a .simex is handed back for the installer.
	simex, _ := a.DropFiles([]string{library, pack, text}, "home", "", "")
	a.DropFiles([]string{library}, "home", "", "")
	if folders := a.localFolders(); simex != pack || len(folders) != 1 || folders[0] != library {
		t.Fatalf("folder import: %v, simex %q", folders, simex)
	}
	// Audio dropped on a local directory is copied in, never over an existing file; other files are ignored.
	if _, refresh := a.DropFiles([]string{song, text}, "menu", "local", library); !refresh {
		t.Fatal("the directory shown must refresh after an import")
	}
	if _, err := os.Stat(filepath.Join(library, "song.mp3")); err != nil {
		t.Fatal("audio was not copied into the directory")
	}
	if _, err := os.Stat(filepath.Join(library, "notes.txt")); err == nil {
		t.Fatal("a file outside the audio extensions was imported")
	}
	if _, refresh := a.DropFiles([]string{song}, "menu", "local", library); refresh {
		t.Fatal("an existing file must be left alone")
	}
	if _, refresh := a.DropFiles([]string{song}, "menu", "local", outside); refresh {
		t.Fatal("only directories of the local extension accept files")
	}
	// Audio dropped on a playlist is added as local songs, without duplicates.
	id := a.Lists.Create("list")
	a.DropFiles([]string{song, text}, "playlist", "", id)
	a.DropFiles([]string{song}, "playlist", "", id)
	if tracks := a.Lists.All()[0].Tracks; len(tracks) != 1 || tracks[0].Source != "local" || tracks[0].ID != song {
		t.Fatalf("playlist import: %v", tracks)
	}
	// Elsewhere, audio files do nothing.
	if _, refresh := a.DropFiles([]string{song}, "settings", "", ""); refresh {
		t.Fatal("audio dropped on another page must be ignored")
	}
}
