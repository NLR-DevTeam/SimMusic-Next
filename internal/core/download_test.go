package core

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
	"simmusic/internal/config"
	"simmusic/internal/model"
)

func downloadFixture(t *testing.T, data []byte, badCover bool) (*Downloader, *DownloadTask) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var cover bytes.Buffer
	if err := png.Encode(&cover, img); err != nil {
		t.Fatal(err)
	}
	jsBytes := func(b []byte) string {
		values := make([]int, len(b))
		for i, v := range b {
			values[i] = int(v)
		}
		encoded, _ := json.Marshal(values)
		return "Uint8Array.from(" + string(encoded) + ")"
	}
	coverData := jsBytes(cover.Bytes())
	if badCover {
		coverData = `"not an image"`
	}
	a, _ := musicFixture(t, `exports.getMusicInfo = (ids, scene) => ids.map(id => scene === "list"
? {id, title:"测试标题", artist:"测试歌手", album:"测试专辑", duration:60, cover:new File([`+coverData+`], "small.png")}
: scene === "download" ? {id, ext:"MP3", download:new File([`+jsBytes(data)+`], "audio.MP3"), largeCover:new File(["broken cover"], "large.jpg")}
: {id, playback:new File([`+jsBytes(data)+`], "audio.MP3"), lyrics:"[00:01.00]测试歌词"});`, nil)
	a.Cfg = config.Open(filepath.Join(a.DataDir, "config.json"), Defaults)
	a.Cfg.Set("down.dir", filepath.Join(a.DataDir, "downloads"))
	return newDownloader(a), &DownloadTask{Track: model.Track{Source: "test", ID: "song"}}
}

func TestDownloadMP3MetadataAndMalformedOriginalTags(t *testing.T) {
	audio := bytes.Repeat([]byte{255, 251, 144, 100}, 256)
	// Valid size/boundary, but TIT2 declares a body larger than the old tag.
	broken := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 10, 'T', 'I', 'T', '2', 0, 0, 0, 100, 0, 0}
	v22 := []byte{'I', 'D', '3', 2, 0, 0, 0, 0, 0, 6, 'T', 'T', '2', 0, 0, 0}
	for _, tc := range []struct {
		name   string
		header []byte
	}{
		{"no tags", nil}, {"malformed frames", broken}, {"ID3v2.2", v22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, task := downloadFixture(t, append(append([]byte{}, tc.header...), audio...), false)
			if err := d.download(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			if filepath.Ext(task.Path) != ".mp3" {
				t.Fatal("uppercase extension not normalized", task.Path)
			}
			tag, err := id3v2.Open(task.Path, id3v2.Options{Parse: true})
			if err != nil {
				t.Fatal(err)
			}
			if tag.Title() != "测试标题" || tag.Artist() != "测试歌手" || tag.Album() != "测试专辑" {
				t.Fatal("missing metadata")
			}
			frames := tag.GetFrames(tag.CommonID("Attached picture"))
			if len(frames) != 1 {
				t.Fatal("thumbnail fallback missing")
			}
			picture := frames[0].(id3v2.PictureFrame)
			if picture.MimeType != "image/png" {
				t.Fatal(picture.MimeType)
			}
			if _, err := png.Decode(bytes.NewReader(picture.Picture)); err != nil {
				t.Fatal(err)
			}
			lyrics := tag.GetFrames(tag.CommonID("Unsynchronised lyrics/text transcription"))
			if len(lyrics) != 1 || lyrics[0].(id3v2.UnsynchronisedLyricsFrame).Lyrics != "[00:01.00]测试歌词" {
				t.Fatal("play-scene lyrics fallback missing")
			}
			tag.Close()
			file, err := os.Open(task.Path)
			if err != nil {
				t.Fatal(err)
			}
			offset, err := mp3AudioOffset(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			result, _ := os.ReadFile(task.Path)
			if !bytes.Equal(result[offset:], audio) {
				t.Fatal("audio bytes changed while rebuilding tags")
			}
		})
	}
}

func TestDownloadMetadataFailuresDoNotReportSuccess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		badCover bool
	}{
		{"invalid boundary", []byte{'I', 'D', '3', 4, 0, 0, 127, 127, 127, 127, 255, 251, 144, 100}, false},
		{"all covers invalid", bytes.Repeat([]byte{255, 251, 144, 100}, 256), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, task := downloadFixture(t, tc.data, tc.badCover)
			if err := d.download(context.Background(), task); err == nil {
				t.Fatal("metadata failure was swallowed")
			}
			if task.Path != "" {
				t.Fatal("partial download published")
			}
			files, err := os.ReadDir(d.a.DownloadDir())
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary/untagged files left: %v %v", files, err)
			}
		})
	}
}

func TestDownloadConvertsCoverAndSidecarLyrics(t *testing.T) {
	d, task := downloadFixture(t, bytes.Repeat([]byte{255, 251, 144, 100}, 256), false)
	d.a.Cfg.Set("down.lyricMode", "file")
	img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	p := d.a.Plugins.Get("test")
	path := filepath.Join(p.DataDir, "cover.gif")
	if err := os.MkdirAll(p.DataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	cover, mime, err := d.downloadCover(model.Track{Source: "test", Cover: "file:" + path})
	if err != nil || mime != "image/png" {
		t.Fatalf("cover conversion: %s %v", mime, err)
	}
	if _, err := png.Decode(bytes.NewReader(cover)); err != nil {
		t.Fatal(err)
	}
	if err := d.download(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	lyrics, err := os.ReadFile(task.lrcPath)
	if err != nil || string(lyrics) != "[00:01.00]测试歌词" {
		t.Fatalf("sidecar: %q %v", lyrics, err)
	}
}

func TestWriteID3ReplacementFailureLeavesOriginalIntact(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows file sharing semantics")
	}
	data := bytes.Repeat([]byte{255, 251, 144, 100}, 256)
	d, task := downloadFixture(t, data, false)
	d.a.Cfg.Set("down.writeCover", false)
	path := filepath.Join(t.TempDir(), "locked.mp3")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	locked, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if err := d.writeID3(path, task.Track, "[00:01]歌词", true); err == nil {
		t.Fatal("replacement error was swallowed")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("failed write changed the original")
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatal("tag temporary file left behind", files, err)
	}
}

func TestWriteID3PreservesDisabledFieldsWithoutInvalidUTF16(t *testing.T) {
	id3 := id3v2.NewEmptyTag()
	id3.SetVersion(3)
	id3.AddTextFrame("TPE1", id3v2.EncodingUTF16, "王靖雯")
	id3.AddTextFrame("TALB", id3v2.EncodingUTF16, "沦陷")
	var data bytes.Buffer
	if _, err := id3.WriteTo(&data); err != nil {
		t.Fatal(err)
	}
	data.Write(bytes.Repeat([]byte{255, 251, 144, 100}, 256))
	d, task := downloadFixture(t, data.Bytes(), false)
	d.a.Cfg.Set("down.writeArtist", false)
	if err := d.download(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	meta, err := tag.ReadFrom(file)
	if err != nil {
		t.Fatalf("preserved UTF-16 still breaks general readers: %v", err)
	}
	if meta.Artist() != "王靖雯" || meta.Album() != "沦陷" || meta.Title() != "测试标题" || meta.Picture() == nil || meta.Lyrics() != "[00:01.00]测试歌词" {
		t.Fatal("preserved or new metadata lost")
	}
	parsed, err := id3v2.Open(task.Path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	for _, key := range []string{"TPE1", "TALB"} {
		if f := parsed.GetTextFrame(key); f.Encoding.Key != 3 {
			t.Fatal("preserved field was not normalized", key)
		}
	}
}
