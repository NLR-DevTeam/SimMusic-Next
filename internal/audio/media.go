package audio

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
)

type fileTags struct {
	title, artist, album, lyrics string
	cover                        []byte
}

// Use the general reader for all formats. Some ID3 writers leave one extra zero
// after UTF-16 text; the general reader rejects the entire tag, including good
// title/cover/lyrics frames. The ID3 reader already used by our writer handles
// that padding, so use it when an ID3 file fails the general reader.
func readFileTags(path string) (fileTags, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileTags{}, err
	}
	defer file.Close()
	metadata, parseErr := tag.ReadFrom(file)
	if parseErr == nil {
		out := fileTags{title: metadata.Title(), artist: metadata.Artist(), album: metadata.Album(), lyrics: metadata.Lyrics()}
		if picture := metadata.Picture(); picture != nil {
			out.cover = picture.Data
		}
		if out.lyrics == "" {
			for _, key := range []string{"LYRICS", "lyrics", "UNSYNCEDLYRICS"} {
				if value, ok := metadata.Raw()[key].(string); ok && value != "" {
					out.lyrics = value
					break
				}
			}
		}
		return out, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fileTags{}, err
	}
	var header [3]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || string(header[:]) != "ID3" {
		return fileTags{}, parseErr
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fileTags{}, err
	}
	id3, err := id3v2.ParseReader(file, id3v2.Options{Parse: true})
	if err != nil {
		return fileTags{}, parseErr
	}
	out := fileTags{title: id3.Title(), artist: id3.Artist(), album: id3.Album()}
	for _, frame := range id3.GetFrames(id3.CommonID("Attached picture")) {
		if picture, ok := frame.(id3v2.PictureFrame); ok && len(picture.Picture) > 0 {
			if len(out.cover) == 0 || picture.PictureType == id3v2.PTFrontCover {
				out.cover = picture.Picture
			}
			if picture.PictureType == id3v2.PTFrontCover {
				break
			}
		}
	}
	for _, frame := range id3.GetFrames(id3.CommonID("Unsynchronised lyrics/text transcription")) {
		if lyrics, ok := frame.(id3v2.UnsynchronisedLyricsFrame); ok && lyrics.Lyrics != "" {
			out.lyrics = lyrics.Lyrics
			break
		}
	}
	if out.lyrics == "" {
		for _, frame := range id3.GetFrames(id3.CommonID("User defined text information frame")) {
			if text, ok := frame.(id3v2.UserDefinedTextFrame); ok && (strings.EqualFold(text.Description, "LYRICS") || strings.EqualFold(text.Description, "UNSYNCEDLYRICS")) {
				out.lyrics = text.Value
				break
			}
		}
	}
	return out, nil
}

// Meta 是本地音频文件的元数据。
type Meta struct {
	Path     string  `json:"path"`
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Album    string  `json:"album"`
	Duration float64 `json:"duration"`
	HasCover bool    `json:"hasCover"`
	Size     int64   `json:"size"`
	Mtime    int64   `json:"mtime"`
	Error    string  `json:"error,omitempty"`
}

// ReadMeta 读取标签与时长。
func ReadMeta(path string) Meta {
	m := ReadTags(path)
	if m.Mtime == 0 {
		return m
	}
	if dec, fm, c, err := open(Source{Path: path}); err == nil {
		m.Duration = float64(dec.Len()) / float64(fm.SampleRate)
		dec.Close()
		c.Close()
	} else {
		m.Error = err.Error()
	}
	return m
}

// ReadTags 只读取标签，不解码音频。
func ReadTags(path string) Meta {
	m := Meta{Path: path}
	st, err := os.Stat(path)
	if err != nil {
		m.Error = err.Error()
		return m
	}
	m.Size, m.Mtime = st.Size(), st.ModTime().Unix()
	if t, err := readFileTags(path); err == nil {
		m.Title = strings.TrimSpace(t.title)
		m.Artist = strings.TrimSpace(t.artist)
		m.Album = strings.TrimSpace(t.album)
		m.HasCover = len(t.cover) > 0
	}
	if m.Title == "" {
		m.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return m
}

// ReadCover 返回内嵌封面；没有时尝试同目录的 cover/folder 图片。
func ReadCover(path string) []byte {
	if t, err := readFileTags(path); err == nil && len(t.cover) > 0 {
		return t.cover
	}
	dir := filepath.Dir(path)
	for _, n := range []string{"cover.jpg", "cover.png", "folder.jpg", "folder.png", "front.jpg"} {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			return b
		}
	}
	return nil
}

// ReadEmbeddedLyrics 返回内嵌歌词（USLT / LYRICS）。
func ReadEmbeddedLyrics(path string) string {
	t, err := readFileTags(path)
	if err != nil {
		return ""
	}
	return t.lyrics
}
