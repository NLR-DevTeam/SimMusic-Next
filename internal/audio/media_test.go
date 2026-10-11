package audio

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
)

func TestReadID3WithOddUTF16Padding(t *testing.T) {
	for _, version := range []byte{3, 4} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			var cover bytes.Buffer
			if err := png.Encode(&cover, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			lyrics := "[00:01.00]测试歌词"
			id3 := id3v2.NewEmptyTag()
			id3.SetVersion(version)
			id3.SetDefaultEncoding(id3v2.EncodingUTF8)
			id3.SetTitle("测试标题")
			// Matches the supplied MP3: UTF-8 title, preserved UTF-16 artist/album
			// with one extra zero from the writer, plus good picture and lyrics.
			id3.AddTextFrame("TPE1", id3v2.EncodingUTF16, "王靖雯")
			id3.AddTextFrame("TALB", id3v2.EncodingUTF16, "沦陷")
			id3.AddAttachedPicture(id3v2.PictureFrame{Encoding: id3v2.EncodingUTF8, MimeType: "image/png", PictureType: id3v2.PTFrontCover, Picture: cover.Bytes()})
			id3.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{Encoding: id3v2.EncodingUTF8, Language: "chi", Lyrics: lyrics})
			var data bytes.Buffer
			if _, err := id3.WriteTo(&data); err != nil {
				t.Fatal(err)
			}
			if _, err := tag.ReadFrom(bytes.NewReader(data.Bytes())); err == nil {
				t.Fatal("fixture must reproduce the strict reader failure")
			}
			path := filepath.Join(t.TempDir(), "metadata.mp3")
			if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			meta := ReadTags(path)
			if meta.Title != "测试标题" || meta.Artist != "王靖雯" || meta.Album != "沦陷" || !meta.HasCover {
				t.Fatalf("metadata lost: %+v", meta)
			}
			if got := ReadCover(path); !bytes.Equal(got, cover.Bytes()) {
				t.Fatal("embedded cover lost")
			}
			if got := ReadEmbeddedLyrics(path); got != lyrics {
				t.Fatalf("lyrics lost: %q", got)
			}
		})
	}
}

func TestReadTagsKeepsNormalAndTaglessBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untagged.mp3")
	if err := os.WriteFile(path, bytes.Repeat([]byte{255, 251, 144, 100}, 256), 0600); err != nil {
		t.Fatal(err)
	}
	if meta := ReadTags(path); meta.Title != "untagged" || meta.HasCover || meta.Artist != "" {
		t.Fatalf("tagless fallback changed: %+v", meta)
	}
	if got := ReadEmbeddedLyrics(path); got != "" {
		t.Fatal("invented lyrics")
	}
	if got := ReadCover(path); len(got) != 0 {
		t.Fatal("invented cover")
	}
	id3 := id3v2.NewEmptyTag()
	id3.SetDefaultEncoding(id3v2.EncodingUTF8)
	id3.SetTitle("标题")
	id3.SetArtist("歌手")
	id3.SetAlbum("专辑")
	var data bytes.Buffer
	if _, err := id3.WriteTo(&data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if meta := ReadTags(path); meta.Title != "标题" || meta.Artist != "歌手" || meta.Album != "专辑" {
		t.Fatalf("normal reader changed: %+v", meta)
	}
}
