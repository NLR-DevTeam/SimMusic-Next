package audio

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
	_ "golang.org/x/image/webp"
)

// Picture describes the cover embedded in an audio file.
type Picture struct {
	Mime   string `json:"mime"`
	Type   string `json:"type"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Size   int    `json:"size"`
	Base64 string `json:"base64,omitempty"`
}

// Tags is the editable metadata of a local audio file.
type Tags struct {
	Path        string   `json:"path"`
	Format      string   `json:"format"`
	Writable    bool     `json:"writable"`
	Title       string   `json:"title"`
	Artist      string   `json:"artist"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"albumArtist"`
	Composer    string   `json:"composer"`
	Lyricist    string   `json:"lyricist"`
	Genre       string   `json:"genre"`
	Year        string   `json:"year"`
	Track       string   `json:"track"`
	Disc        string   `json:"disc"`
	Comment     string   `json:"comment"`
	Lyrics      string   `json:"lyrics"`
	Cover       *Picture `json:"cover"`
}

// TagFields lists the text fields WriteTags accepts.
var TagFields = []string{"title", "artist", "album", "albumArtist", "composer", "lyricist", "genre", "year", "track", "disc", "comment", "lyrics"}

// TagChanges holds the fields to rewrite; fields not present keep their value and an empty
// string deletes one. SetCover replaces the embedded pictures, an empty Cover removes them.
type TagChanges struct {
	Text     map[string]string
	SetCover bool
	Cover    []byte
}

var ErrTagFormat = errors.New("此格式不支持写入元数据")

var pictureTypes = []string{"Other", "File Icon", "Other File Icon", "Front Cover", "Back Cover", "Leaflet Page", "Media", "Lead Artist", "Artist", "Conductor", "Band", "Composer", "Lyricist", "Recording Location", "During Recording", "During Performance", "Video Screen Capture", "Bright Coloured Fish", "Illustration", "Band Logotype", "Publisher Logotype"}

func pictureInfo(data []byte, kind int, withData bool) *Picture {
	if len(data) == 0 {
		return nil
	}
	p := &Picture{Mime: http.DetectContentType(data), Type: "Other", Size: len(data)}
	if kind >= 0 && kind < len(pictureTypes) {
		p.Type = pictureTypes[kind]
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		p.Width, p.Height = cfg.Width, cfg.Height
	}
	if withData {
		p.Base64 = base64.StdEncoding.EncodeToString(data)
	}
	return p
}

func tagFormat(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	offset, _ := mp3AudioOffset(file)
	var magic [4]byte
	if _, err := file.ReadAt(magic[:], offset); err == nil {
		switch string(magic[:]) {
		case "fLaC":
			return "flac"
		case "OggS":
			return "ogg"
		}
	}
	if strings.EqualFold(filepath.Ext(path), ".mp3") {
		return "mp3"
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// ReadFullTags reads every editable field; withCover also returns the picture data.
func ReadFullTags(path string, withCover bool) (Tags, error) {
	if _, err := os.Stat(path); err != nil {
		return Tags{}, err
	}
	out := Tags{Path: path, Format: tagFormat(path)}
	switch out.Format {
	case "mp3":
		out.Writable = true
		if err := readID3(path, &out, withCover); err == nil {
			return out, nil
		}
	case "flac":
		out.Writable = true
		if err := readFlacTags(path, &out, withCover); err != nil {
			return out, err
		}
		return out, nil
	}
	readGenericTags(path, &out, withCover)
	return out, nil
}

func readGenericTags(path string, out *Tags, withCover bool) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	m, err := tag.ReadFrom(file)
	if err != nil {
		return
	}
	pair := func(n, total int) string {
		if n == 0 {
			return ""
		}
		if total > 0 {
			return fmt.Sprintf("%d/%d", n, total)
		}
		return strconv.Itoa(n)
	}
	out.Title, out.Artist, out.Album, out.AlbumArtist = m.Title(), m.Artist(), m.Album(), m.AlbumArtist()
	out.Composer, out.Genre, out.Lyrics, out.Comment = m.Composer(), m.Genre(), m.Lyrics(), m.Comment()
	if m.Year() > 0 {
		out.Year = strconv.Itoa(m.Year())
	}
	out.Track, out.Disc = pair(m.Track()), pair(m.Disc())
	for _, key := range []string{"lyricist", "LYRICIST", "TEXT"} {
		if value, ok := m.Raw()[key].(string); ok && value != "" {
			out.Lyricist = value
			break
		}
	}
	if picture := m.Picture(); picture != nil {
		out.Cover = pictureInfo(picture.Data, 3, withCover)
	}
}

// ---------- ID3v2 ----------

var id3Text = map[string]string{"title": "TIT2", "artist": "TPE1", "album": "TALB", "albumArtist": "TPE2", "composer": "TCOM", "lyricist": "TEXT", "genre": "TCON", "track": "TRCK", "disc": "TPOS"}

func readID3(path string, out *Tags, withCover bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	t, err := id3v2.ParseReader(file, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	text := func(id string) string { return strings.TrimRight(t.GetTextFrame(id).Text, "\x00") }
	out.Title, out.Artist, out.Album, out.AlbumArtist = text("TIT2"), text("TPE1"), text("TALB"), text("TPE2")
	out.Composer, out.Lyricist, out.Genre = text("TCOM"), text("TEXT"), text("TCON")
	out.Track, out.Disc = text("TRCK"), text("TPOS")
	if out.Year = text("TDRC"); out.Year == "" {
		out.Year = text("TYER")
	}
	for _, frame := range t.GetFrames("COMM") {
		if comment, ok := frame.(id3v2.CommentFrame); ok && comment.Text != "" {
			out.Comment = comment.Text
			break
		}
	}
	for _, frame := range t.GetFrames("USLT") {
		if lyrics, ok := frame.(id3v2.UnsynchronisedLyricsFrame); ok && lyrics.Lyrics != "" {
			out.Lyrics = lyrics.Lyrics
			break
		}
	}
	var cover *id3v2.PictureFrame
	for _, frame := range t.GetFrames("APIC") {
		if picture, ok := frame.(id3v2.PictureFrame); ok && len(picture.Picture) > 0 {
			if cover == nil || picture.PictureType == id3v2.PTFrontCover {
				cover = &picture
			}
			if picture.PictureType == id3v2.PTFrontCover {
				break
			}
		}
	}
	if cover != nil {
		out.Cover = pictureInfo(cover.Picture, int(cover.PictureType), withCover)
	}
	return nil
}

// mp3AudioOffset returns the size of a leading ID3v2 tag: where the audio data begins.
func mp3AudioOffset(file *os.File) (int64, error) {
	header := make([]byte, 10)
	n, err := file.ReadAt(header, 0)
	if n < 3 || string(header[:3]) != "ID3" {
		return 0, nil
	}
	if n < 10 || err != nil && err != io.EOF || header[3] < 2 || header[3] > 4 {
		return 0, errors.New("无效 ID3 标签头")
	}
	var size int64
	for _, b := range header[6:10] {
		if b&128 != 0 {
			return 0, errors.New("无效 ID3 标签大小")
		}
		size = size<<7 | int64(b)
	}
	size += 10
	if header[3] == 4 && header[5]&16 != 0 {
		size += 10
	}
	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if size > stat.Size() {
		return 0, errors.New("ID3 标签超出文件大小")
	}
	return size, nil
}

func writeID3(path string, c TagChanges) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	offset, err := mp3AudioOffset(src)
	if err != nil {
		return err
	}
	t, err := id3v2.ParseReader(src, id3v2.Options{Parse: true})
	if err != nil {
		// Frames that cannot be parsed are rebuilt; the audio boundary is still known.
		t = id3v2.NewEmptyTag()
	}
	t.SetVersion(4)
	t.SetDefaultEncoding(id3v2.EncodingUTF8)
	// Preserved UTF-16 text is saved as UTF-8: this library pads re-encoded UTF-16 so that
	// strict readers discard the whole tag.
	for key, frames := range t.AllFrames() {
		for _, frame := range frames {
			switch text := frame.(type) {
			case id3v2.TextFrame:
				text.Encoding = id3v2.EncodingUTF8
				t.AddFrame(key, text)
			case id3v2.UserDefinedTextFrame:
				text.Encoding = id3v2.EncodingUTF8
				t.AddFrame(key, text)
			case id3v2.CommentFrame:
				text.Encoding = id3v2.EncodingUTF8
				t.AddFrame(key, text)
			case id3v2.UnsynchronisedLyricsFrame:
				text.Encoding = id3v2.EncodingUTF8
				t.AddFrame(key, text)
			case id3v2.PictureFrame:
				text.Encoding = id3v2.EncodingUTF8
				t.AddFrame(key, text)
			}
		}
	}
	for field, value := range c.Text {
		switch field {
		case "year":
			t.DeleteFrames("TYER")
			t.DeleteFrames("TDRC")
			if value != "" {
				t.AddTextFrame("TDRC", id3v2.EncodingUTF8, value)
			}
		case "comment":
			language := "XXX"
			if old, ok := t.GetLastFrame("COMM").(id3v2.CommentFrame); ok && len(old.Language) == 3 {
				language = old.Language
			}
			t.DeleteFrames("COMM")
			if value != "" {
				t.AddCommentFrame(id3v2.CommentFrame{Encoding: id3v2.EncodingUTF8, Language: language, Text: value})
			}
		case "lyrics":
			language := "chi"
			if old, ok := t.GetLastFrame("USLT").(id3v2.UnsynchronisedLyricsFrame); ok && len(old.Language) == 3 {
				language = old.Language
			}
			t.DeleteFrames("USLT")
			if value != "" {
				t.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{Encoding: id3v2.EncodingUTF8, Language: language, Lyrics: value})
			}
		default:
			id := id3Text[field]
			t.DeleteFrames(id)
			if value != "" {
				t.AddTextFrame(id, id3v2.EncodingUTF8, value)
			}
		}
	}
	if c.SetCover {
		t.DeleteFrames("APIC")
		if len(c.Cover) > 0 {
			t.AddAttachedPicture(id3v2.PictureFrame{Encoding: id3v2.EncodingUTF8, MimeType: http.DetectContentType(c.Cover), PictureType: id3v2.PTFrontCover, Picture: c.Cover})
		}
	}
	return replaceFile(path, func(out *os.File) error {
		if _, err := t.WriteTo(out); err != nil {
			return err
		}
		if _, err := src.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		_, err := io.Copy(out, src)
		return err
	}, src)
}

// replaceFile writes a complete replacement beside the file before touching it.
func replaceFile(path string, write func(*os.File) error, src *os.File) error {
	out, err := os.CreateTemp(filepath.Dir(path), "*.tagtemp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if err = write(out); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = src.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}

// ---------- FLAC ----------

var vorbisKeys = map[string][]string{
	"title": {"TITLE"}, "artist": {"ARTIST"}, "album": {"ALBUM"}, "albumArtist": {"ALBUMARTIST", "ALBUM ARTIST"},
	"composer": {"COMPOSER"}, "lyricist": {"LYRICIST"}, "genre": {"GENRE"}, "year": {"DATE", "YEAR"},
	"track": {"TRACKNUMBER"}, "disc": {"DISCNUMBER"}, "comment": {"COMMENT", "DESCRIPTION"}, "lyrics": {"LYRICS", "UNSYNCEDLYRICS"},
}

type flacBlock struct {
	kind byte
	data []byte
}

// readFlacBlocks returns the metadata blocks and the offset of the first audio frame.
func readFlacBlocks(file *os.File) ([]flacBlock, int64, error) {
	offset, err := mp3AudioOffset(file)
	if err != nil {
		return nil, 0, err
	}
	var magic [4]byte
	if _, err := file.ReadAt(magic[:], offset); err != nil || string(magic[:]) != "fLaC" {
		return nil, 0, errors.New("不是有效的 FLAC 文件")
	}
	offset += 4
	var blocks []flacBlock
	for {
		var header [4]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			return nil, 0, errors.New("FLAC 元数据不完整")
		}
		size := int64(header[1])<<16 | int64(header[2])<<8 | int64(header[3])
		data := make([]byte, size)
		if _, err := file.ReadAt(data, offset+4); err != nil {
			return nil, 0, errors.New("FLAC 元数据不完整")
		}
		blocks = append(blocks, flacBlock{header[0] & 0x7f, data})
		offset += 4 + size
		if header[0]&0x80 != 0 {
			return blocks, offset, nil
		}
	}
}

func parseVorbis(data []byte) (vendor string, comments []string) {
	next := func() (string, bool) {
		if len(data) < 4 {
			return "", false
		}
		n := int(binary.LittleEndian.Uint32(data))
		if n < 0 || n > len(data)-4 {
			return "", false
		}
		s := string(data[4 : 4+n])
		data = data[4+n:]
		return s, true
	}
	vendor, ok := next()
	if !ok || len(data) < 4 {
		return vendor, nil
	}
	count := binary.LittleEndian.Uint32(data)
	data = data[4:]
	for i := uint32(0); i < count; i++ {
		s, ok := next()
		if !ok {
			break
		}
		comments = append(comments, s)
	}
	return vendor, comments
}

func buildVorbis(vendor string, comments []string) []byte {
	var b bytes.Buffer
	put := func(s string) {
		binary.Write(&b, binary.LittleEndian, uint32(len(s)))
		b.WriteString(s)
	}
	put(vendor)
	binary.Write(&b, binary.LittleEndian, uint32(len(comments)))
	for _, c := range comments {
		put(c)
	}
	return b.Bytes()
}

func vorbisField(key string) string {
	key = strings.ToUpper(key)
	for field, names := range vorbisKeys {
		for _, name := range names {
			if name == key {
				return field
			}
		}
	}
	return ""
}

func flacPicture(data []byte) (kind int, picture []byte) {
	// type, mime, description, width, height, depth, colours, data
	read := func() (uint32, bool) {
		if len(data) < 4 {
			return 0, false
		}
		v := binary.BigEndian.Uint32(data)
		data = data[4:]
		return v, true
	}
	skip := func() bool {
		n, ok := read()
		if !ok || int(n) > len(data) {
			return false
		}
		data = data[n:]
		return true
	}
	t, ok := read()
	if !ok || !skip() || !skip() || len(data) < 20 {
		return 0, nil
	}
	data = data[16:]
	n, _ := read()
	if int(n) > len(data) {
		return 0, nil
	}
	return int(t), data[:n]
}

func readFlacTags(path string, out *Tags, withCover bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	blocks, _, err := readFlacBlocks(file)
	if err != nil {
		return err
	}
	values := map[string][]string{}
	var cover []byte
	coverKind := -1
	for _, block := range blocks {
		switch block.kind {
		case 4:
			_, comments := parseVorbis(block.data)
			for _, c := range comments {
				key, value, _ := strings.Cut(c, "=")
				if field := vorbisField(key); field != "" && value != "" {
					values[field] = append(values[field], value)
				}
			}
		case 6:
			if kind, data := flacPicture(block.data); len(data) > 0 && (cover == nil || kind == 3 && coverKind != 3) {
				cover, coverKind = data, kind
			}
		}
	}
	get := func(field string) string { return strings.Join(values[field], "; ") }
	out.Title, out.Artist, out.Album, out.AlbumArtist = get("title"), get("artist"), get("album"), get("albumArtist")
	out.Composer, out.Lyricist, out.Genre, out.Year = get("composer"), get("lyricist"), get("genre"), get("year")
	out.Track, out.Disc, out.Comment = get("track"), get("disc"), get("comment")
	if len(values["lyrics"]) > 0 {
		out.Lyrics = values["lyrics"][0]
	}
	out.Cover = pictureInfo(cover, coverKind, withCover)
	return nil
}

func writeFlac(path string, c TagChanges) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	blocks, audioOffset, err := readFlacBlocks(src)
	if err != nil {
		return err
	}
	vendor := "SimMusic Next"
	var comments []string
	var kept []flacBlock
	for _, block := range blocks {
		switch {
		case block.kind == 4:
			vendor, comments = parseVorbis(block.data)
		case block.kind == 1, block.kind == 6 && c.SetCover:
		default:
			kept = append(kept, block)
		}
	}
	if len(kept) == 0 || kept[0].kind != 0 {
		return errors.New("不是有效的 FLAC 文件")
	}
	var next []string
	for _, comment := range comments {
		key, _, _ := strings.Cut(comment, "=")
		if _, changed := c.Text[vorbisField(key)]; !changed {
			next = append(next, comment)
		}
	}
	for _, field := range TagFields {
		if value, ok := c.Text[field]; ok && value != "" {
			next = append(next, vorbisKeys[field][0]+"="+value)
		}
	}
	kept = append(kept, flacBlock{4, buildVorbis(vendor, next)})
	if c.SetCover && len(c.Cover) > 0 {
		var b bytes.Buffer
		mime := http.DetectContentType(c.Cover)
		var width, height uint32
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(c.Cover)); err == nil {
			width, height = uint32(cfg.Width), uint32(cfg.Height)
		}
		for _, v := range []any{uint32(3), uint32(len(mime)), []byte(mime), uint32(0), width, height, uint32(0), uint32(0), uint32(len(c.Cover)), c.Cover} {
			binary.Write(&b, binary.BigEndian, v)
		}
		kept = append(kept, flacBlock{6, b.Bytes()})
	}
	kept = append(kept, flacBlock{1, make([]byte, 2048)})
	for _, block := range kept {
		if len(block.data) >= 1<<24 {
			return errors.New("封面图片过大，无法写入 FLAC")
		}
	}
	return replaceFile(path, func(out *os.File) error {
		if _, err := out.WriteString("fLaC"); err != nil {
			return err
		}
		for i, block := range kept {
			header := []byte{block.kind, byte(len(block.data) >> 16), byte(len(block.data) >> 8), byte(len(block.data))}
			if i == len(kept)-1 {
				header[0] |= 0x80
			}
			if _, err := out.Write(append(header, block.data...)); err != nil {
				return err
			}
		}
		if _, err := src.Seek(audioOffset, io.SeekStart); err != nil {
			return err
		}
		_, err := io.Copy(out, src)
		return err
	}, src)
}

// WriteTags rewrites the given fields of an MP3 or FLAC file, keeping every audio byte.
func WriteTags(path string, c TagChanges) error {
	for field := range c.Text {
		if _, ok := vorbisKeys[field]; !ok {
			return fmt.Errorf("未知的元数据字段：%s", field)
		}
	}
	if c.SetCover && len(c.Cover) > 0 && !strings.HasPrefix(http.DetectContentType(c.Cover), "image/") {
		return errors.New("封面不是有效的图片")
	}
	switch tagFormat(path) {
	case "mp3":
		return writeID3(path, c)
	case "flac":
		return writeFlac(path, c)
	}
	return ErrTagFormat
}
