package audio

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
)

func testCover(t *testing.T, w, h int) []byte {
	var cover bytes.Buffer
	if err := png.Encode(&cover, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return cover.Bytes()
}

var allTagText = map[string]string{"title": "以风之名爱你", "artist": "歌手", "album": "专辑", "albumArtist": "专辑艺术家", "composer": "作曲", "lyricist": "作词", "genre": "流行", "year": "2024", "track": "3/12", "disc": "1", "comment": "注释", "lyrics": "[00:00.00]以风之名爱你"}

func checkTagText(t *testing.T, got Tags, want map[string]string) {
	t.Helper()
	have := map[string]string{"title": got.Title, "artist": got.Artist, "album": got.Album, "albumArtist": got.AlbumArtist, "composer": got.Composer, "lyricist": got.Lyricist, "genre": got.Genre, "year": got.Year, "track": got.Track, "disc": got.Disc, "comment": got.Comment, "lyrics": got.Lyrics}
	for field, value := range want {
		if have[field] != value {
			t.Errorf("%s = %q, want %q", field, have[field], value)
		}
	}
}

func testTagRoundTrip(t *testing.T, path string, audio []byte) {
	cover := testCover(t, 4, 3)
	if err := WriteTags(path, TagChanges{Text: allTagText, SetCover: true, Cover: cover}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFullTags(path, true)
	if err != nil {
		t.Fatal(err)
	}
	checkTagText(t, got, allTagText)
	if !got.Writable || got.Cover == nil || got.Cover.Mime != "image/png" || got.Cover.Width != 4 || got.Cover.Height != 3 || got.Cover.Type != "Front Cover" || got.Cover.Size != len(cover) || got.Cover.Base64 != base64.StdEncoding.EncodeToString(cover) {
		t.Fatalf("cover lost: %+v", got.Cover)
	}
	// The file stays readable for the general reader the index uses.
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	general, err := tag.ReadFrom(file)
	file.Close()
	if err != nil || general.Title() != allTagText["title"] || general.Picture() == nil {
		t.Fatalf("general reader: %v", err)
	}
	// Only the named fields change; an empty value deletes, the cover is kept unless set.
	if err := WriteTags(path, TagChanges{Text: map[string]string{"title": "新标题", "genre": ""}}); err != nil {
		t.Fatal(err)
	}
	got, _ = ReadFullTags(path, false)
	want := map[string]string{}
	for k, v := range allTagText {
		want[k] = v
	}
	want["title"], want["genre"] = "新标题", ""
	checkTagText(t, got, want)
	if got.Cover == nil || got.Cover.Base64 != "" {
		t.Fatalf("cover should be kept without data: %+v", got.Cover)
	}
	if err := WriteTags(path, TagChanges{SetCover: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ = ReadFullTags(path, false); got.Cover != nil || got.Title != "新标题" {
		t.Fatalf("cover not removed: %+v", got)
	}
	data, _ := os.ReadFile(path)
	if !bytes.HasSuffix(data, audio) {
		t.Fatal("audio data changed")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatal("temporary file left behind")
	}
}

func TestWriteTagsMP3(t *testing.T) {
	audio := bytes.Repeat([]byte{255, 251, 144, 100}, 256)
	for name, prefix := range map[string]func() []byte{
		"untagged": func() []byte { return nil },
		"utf16": func() []byte {
			id3 := id3v2.NewEmptyTag()
			id3.SetVersion(3)
			id3.AddTextFrame("TPE1", id3v2.EncodingUTF16, "旧歌手")
			id3.AddTextFrame("TXXX", id3v2.EncodingUTF16, "keep")
			var b bytes.Buffer
			id3.WriteTo(&b)
			return b.Bytes()
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "song.mp3")
			if err := os.WriteFile(path, append(prefix(), audio...), 0600); err != nil {
				t.Fatal(err)
			}
			testTagRoundTrip(t, path, audio)
		})
	}
}

func TestWriteTagsFLAC(t *testing.T) {
	audio := bytes.Repeat([]byte{0xff, 0xf8, 1, 2, 3}, 100)
	var file bytes.Buffer
	file.WriteString("fLaC")
	file.Write(append([]byte{0, 0, 0, 34}, make([]byte, 34)...))
	file.Write(append([]byte{4, 0, 0, 0}, buildVorbis("vendor", []string{"ARTIST=旧歌手", "REPLAYGAIN_TRACK_GAIN=-6 dB"})...))
	file.Bytes()[4+38+3] = byte(len(buildVorbis("vendor", []string{"ARTIST=旧歌手", "REPLAYGAIN_TRACK_GAIN=-6 dB"})))
	file.Write([]byte{0x81, 0, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0})
	file.Write(audio)
	path := filepath.Join(t.TempDir(), "song.flac")
	if err := os.WriteFile(path, file.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	testTagRoundTrip(t, path, audio)
	handle, _ := os.Open(path)
	defer handle.Close()
	blocks, _, err := readFlacBlocks(handle)
	if err != nil || blocks[0].kind != 0 {
		t.Fatalf("stream info must stay first: %v", err)
	}
	for _, block := range blocks {
		if block.kind == 4 {
			vendor, comments := parseVorbis(block.data)
			if vendor != "vendor" || !bytes.Contains([]byte(comments[0]+comments[1]), []byte("REPLAYGAIN_TRACK_GAIN=-6 dB")) {
				t.Fatalf("unrelated comments lost: %q %q", vendor, comments)
			}
		}
	}
}

func TestWriteTagsRejectsOtherFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "song.ogg")
	os.WriteFile(path, []byte("OggS"), 0600)
	if err := WriteTags(path, TagChanges{Text: map[string]string{"title": "x"}}); err != ErrTagFormat {
		t.Fatalf("got %v", err)
	}
	if err := WriteTags(path, TagChanges{Text: map[string]string{"bogus": "x"}}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if tags, err := ReadFullTags(path, false); err != nil || tags.Writable || tags.Format != "ogg" {
		t.Fatalf("%+v %v", tags, err)
	}
}
