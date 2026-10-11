package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogem/id3v2/v2"
	_ "golang.org/x/image/webp"

	"simmusic/internal/model"
)

// 下载任务状态。
const (
	DownloadPending = "pending"
	DownloadRunning = "download"
	DownloadSuccess = "success"
	DownloadError   = "error"
)

// DownloadTask 表示一个下载任务。
type DownloadTask struct {
	ID       int         `json:"id"`
	Track    model.Track `json:"track"`
	Status   string      `json:"status"`
	Text     string      `json:"text"`     // 当前步骤的固定说明
	Progress float64     `json:"progress"` // 0..1
	Path     string      `json:"path"`

	lrcPath string
	cancel  context.CancelFunc
}

type Downloader struct {
	a       *App
	mu      sync.Mutex
	tasks   []*DownloadTask
	running int
	nextID  int
	nameMu  sync.Mutex // one task at a time picks and takes its final file name
}

func newDownloader(a *App) *Downloader { return &Downloader{a: a} }

func (d *Downloader) Tasks() []DownloadTask {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DownloadTask, len(d.tasks))
	for i, t := range d.tasks {
		out[i] = *t
	}
	return out
}

// CanDownload 判断曲目所属插件是否支持下载。
func (a *App) CanDownload(t model.Track) bool {
	s := a.Plugins.Scheme(t.Source)
	return s != nil && s.DownloadAllowed()
}

func (a *App) DownloadDir() string {
	if d := a.Cfg.String("down.dir"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Music", "SimMusic Downloads")
}

func (d *Downloader) find(id int) *DownloadTask {
	for _, t := range d.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (d *Downloader) update(t *DownloadTask, status, text string, progress float64) {
	d.mu.Lock()
	t.Status, t.Text, t.Progress = status, text, progress
	d.mu.Unlock()
	d.a.redraw()
}

// Enqueue 加入下载队列。
func (d *Downloader) Enqueue(ts ...model.Track) {
	added := 0
	d.mu.Lock()
	for _, t := range ts {
		if !d.a.CanDownload(t) {
			continue
		}
		d.nextID++
		d.tasks = append(d.tasks, &DownloadTask{ID: d.nextID, Track: t, Status: DownloadPending, Text: "正在等待下载"})
		added++
	}
	d.mu.Unlock()
	d.a.Toast(fmt.Sprintf("已添加 %d 个下载任务", added))
	d.schedule()
}

func (d *Downloader) schedule() {
	limit, _ := strconv.Atoi(d.a.Cfg.String("down.parallel"))
	limit = max(1, limit)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.tasks {
		if d.running >= limit {
			break
		}
		if t.Status == DownloadPending {
			ctx, cancel := context.WithCancel(context.Background())
			t.Status, t.Text, t.cancel = DownloadRunning, "正在获取下载地址", cancel
			d.running++
			go d.run(ctx, t)
		}
	}
}

func (d *Downloader) run(ctx context.Context, t *DownloadTask) {
	err := d.download(ctx, t)
	d.mu.Lock()
	d.running--
	t.cancel = nil
	removed := d.find(t.ID) != t
	if err != nil {
		t.Status, t.Text, t.Progress = DownloadError, "曲目下载失败", 0
	} else {
		t.Status, t.Text, t.Progress = DownloadSuccess, "曲目下载成功", 1
	}
	d.mu.Unlock()
	switch {
	case removed:
		// The user removed the task while it was running; there is nothing to report.
		if err == nil {
			d.removeFiles(t)
		}
	case err != nil:
		d.a.sourceError(t.Track, "歌曲下载失败", err)
		d.a.Alert("歌曲下载失败：" + trackTitle(t.Track))
	}
	d.a.redraw()
	d.schedule()
}

var illegal = strings.NewReplacer(`\`, "_", "/", "_", ":", "：", "*", "_", "?", "？", `"`, "'", "<", "《", ">", "》", "|", "_")

func (d *Downloader) fileName(t model.Track, ext string) string {
	f := d.a.Cfg.String("down.nameFormat")
	if f == "" {
		f = "[title]"
	}
	name := strings.NewReplacer("[title]", t.Title, "[artist]", t.Artist, "[album]", t.Album).Replace(f)
	name = strings.TrimSpace(illegal.Replace(name))
	if name == "" {
		name = "未命名"
	}
	return name + ext
}

// freePath returns dir/name.ext, or the first "name (n).ext" that does not exist yet.
func freePath(dir, name, ext string) string {
	path := filepath.Join(dir, name+ext)
	for n := 1; ; n++ {
		if _, err := os.Stat(path); err != nil {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, n, ext))
	}
}

func (d *Downloader) download(ctx context.Context, t *DownloadTask) error {
	a := d.a
	if a.Plugins.Scheme(t.Track.Source) == nil {
		return errors.New("音源插件未启用")
	}
	if !a.CanDownload(t.Track) {
		return errors.New("此音源不支持下载")
	}
	info, fresh, err := a.resolveMusicInfo(t.Track, "download")
	if err != nil {
		return err
	}
	d.mu.Lock()
	t.Track = fresh
	d.mu.Unlock()
	resource := info.Download
	if resource.Empty() {
		return errors.New("插件未返回下载地址")
	}
	p := a.resourcePlugin(t.Track.Source, resource.Provider)
	if p == nil {
		return errors.New("下载资源提供者未启用")
	}
	if err := p.ValidateResource(resource); err != nil {
		return err
	}
	ext := strings.ToLower(strings.TrimSpace(info.Ext))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(strings.Split(resource.URL, "?")[0]))
		if resource.Path != "" {
			ext = filepath.Ext(resource.Path)
		}
	}
	if ext == "" {
		ext = ".mp3"
	}
	ext = strings.ToLower(strings.TrimSpace(ext))
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(ext) > 12 || strings.ContainsAny(ext, `/\:`) {
		return errors.New("无效下载扩展名")
	}
	dir := a.DownloadDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	d.update(t, DownloadRunning, "正在连接下载地址", 0)
	var body io.ReadCloser
	var total int64
	if resource.Path != "" {
		file, err := os.Open(resource.Path)
		if err != nil {
			return err
		}
		body = file
		if stat, err := file.Stat(); err == nil {
			total = stat.Size()
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, "GET", resource.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "SimMusicNext/0.1")
		for k, v := range resource.Headers {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		body, total = resp.Body, resp.ContentLength
	}
	defer body.Close()
	f, err := os.CreateTemp(dir, "*.simtemp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	d.update(t, DownloadRunning, "正在下载曲目", 0)
	var got int64
	buf := make([]byte, 64<<10)
	last := time.Now()
	for {
		if ctx.Err() != nil {
			f.Close()
			return ctx.Err()
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			got += int64(n)
			if total > 0 && time.Since(last) > 200*time.Millisecond {
				progress := float64(got) / float64(total)
				d.update(t, DownloadRunning, fmt.Sprintf("正在下载 %d%%", int(progress*100)), progress)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	d.update(t, DownloadRunning, "正在写入元数据", 1)
	mode := a.Cfg.String("down.lyricMode")
	lyr := ""
	if mode == "embed" || mode == "file" {
		lyr, err = a.readMusicLyrics(t.Track.Source, info.Lyrics)
		if err != nil {
			return fmt.Errorf("获取下载歌词：%w", err)
		}
	}
	// 写入标签（仅 MP3 / ID3）；非 MP3 无法嵌入歌词，退化为独立 LRC 文件
	if ext == ".mp3" {
		if err := d.writeID3(tmp, t.Track, lyr, mode == "embed"); err != nil {
			return fmt.Errorf("写入 MP3 标签：%w", err)
		}
	} else if mode == "embed" {
		mode = "file"
	}
	d.update(t, DownloadRunning, "正在保存曲目", 1)
	d.nameMu.Lock()
	defer d.nameMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	dst := freePath(dir, strings.TrimSuffix(d.fileName(t.Track, ext), ext), ext)
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	lrc := ""
	if mode == "file" && lyr != "" {
		lrc = strings.TrimSuffix(dst, ext) + ".lrc"
		if err := os.WriteFile(lrc, []byte(lyr), 0o644); err != nil {
			_ = os.Remove(dst)
			return fmt.Errorf("保存歌词文件：%w", err)
		}
	}
	d.mu.Lock()
	t.Path, t.lrcPath = dst, lrc
	d.mu.Unlock()
	return nil
}

func (d *Downloader) writeID3(path string, t model.Track, lyr string, embedLyrics bool) error {
	cfg := d.a.Cfg
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	offset, err := mp3AudioOffset(src)
	if err != nil {
		return err
	}
	if _, err = src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tag, err := id3v2.ParseReader(src, id3v2.Options{Parse: true})
	if err != nil {
		// A readable tag header still identifies the audio boundary even when old frames
		// are malformed or ID3v2.2. Rebuild tags instead of silently skipping metadata.
		tag = id3v2.NewEmptyTag()
	}
	tag.SetVersion(4)
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)
	// Normalize preserved text frames too: re-encoding UTF-16 with this library
	// adds odd zero padding, making strict readers discard all metadata. Keep the
	// original text when writing that field is disabled, but save it as UTF-8.
	for key, frames := range tag.AllFrames() {
		for _, frame := range frames {
			switch text := frame.(type) {
			case id3v2.TextFrame:
				text.Encoding = id3v2.EncodingUTF8
				tag.AddFrame(key, text)
			case id3v2.UserDefinedTextFrame:
				text.Encoding = id3v2.EncodingUTF8
				tag.AddFrame(key, text)
			case id3v2.CommentFrame:
				text.Encoding = id3v2.EncodingUTF8
				tag.AddFrame(key, text)
			case id3v2.UnsynchronisedLyricsFrame:
				text.Encoding = id3v2.EncodingUTF8
				tag.AddFrame(key, text)
			case id3v2.PictureFrame:
				text.Encoding = id3v2.EncodingUTF8
				tag.AddFrame(key, text)
			}
		}
	}
	if cfg.Bool("down.writeTitle") {
		tag.SetTitle(t.Title)
	}
	if cfg.Bool("down.writeArtist") {
		tag.SetArtist(t.Artist)
		tag.SetAlbum(t.Album)
	}
	if cfg.Bool("down.writeCover") {
		img, mime, err := d.downloadCover(t)
		if err != nil {
			return err
		}
		if len(img) > 0 {
			tag.DeleteFrames(tag.CommonID("Attached picture"))
			tag.AddAttachedPicture(id3v2.PictureFrame{
				Encoding: id3v2.EncodingUTF8, MimeType: mime, PictureType: id3v2.PTFrontCover,
				Description: "Cover", Picture: img,
			})
		}
	}
	if embedLyrics && lyr != "" {
		tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{
			Encoding: id3v2.EncodingUTF8, Language: "chi", ContentDescriptor: "", Lyrics: lyr,
		})
	}
	// Write a complete replacement before touching the downloaded file. Copying from
	// the verified boundary preserves every audio byte, including when old tags broke.
	out, err := os.CreateTemp(filepath.Dir(path), "*.id3temp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if _, err = tag.WriteTo(out); err != nil {
		return err
	}
	if _, err = src.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err = io.Copy(out, src); err != nil {
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

func mp3AudioOffset(file *os.File) (int64, error) {
	header := make([]byte, 10)
	n, err := io.ReadFull(file, header)
	if n < 3 || string(header[:3]) != "ID3" {
		return 0, nil
	}
	if err != nil || header[3] < 2 || header[3] > 4 {
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
		size += 10 // ID3v2.4 footer is outside the header's declared body size.
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

func (d *Downloader) downloadCover(t model.Track) ([]byte, string, error) {
	candidates := []model.Track{t}
	if t.LargeCover != "" {
		large := t
		large.Cover, large.CoverHeaders, large.CoverProvider = t.LargeCover, t.LargeCoverHeaders, t.LargeCoverProvider
		candidates = append([]model.Track{large}, candidates...)
	}
	for i, candidate := range candidates {
		if i > 0 && candidate.Cover == candidates[0].Cover && candidate.CoverProvider == candidates[0].CoverProvider && maps.Equal(candidate.CoverHeaders, candidates[0].CoverHeaders) {
			continue
		}
		data := d.a.CoverBytes(candidate)
		if len(data) == 0 {
			continue
		}
		_, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			continue
		}
		if format == "jpeg" || format == "png" {
			return data, "image/" + format, nil
		}
		// Common tag readers support JPEG/PNG; convert WebP/GIF instead of labelling
		// their bytes as JPEG, which causes readers to discard otherwise valid covers.
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err == nil {
			return buf.Bytes(), "image/png", nil
		}
	}
	if t.Cover != "" || t.LargeCover != "" {
		return nil, "", errors.New("大封面与列表封面均获取失败或图片无效")
	}
	return nil, "", nil
}

// Retry 重新下载失败的任务。
func (d *Downloader) Retry(id int) {
	d.mu.Lock()
	if t := d.find(id); t != nil && t.Status == DownloadError {
		t.Status, t.Text, t.Progress = DownloadPending, "正在等待下载", 0
	}
	d.mu.Unlock()
	d.a.redraw()
	d.schedule()
}

// Remove 从列表中移除任务；正在下载的任务同时取消，已下载的文件保留。
func (d *Downloader) Remove(id int) *DownloadTask {
	d.mu.Lock()
	t := d.find(id)
	if t != nil {
		if t.cancel != nil {
			t.cancel()
		}
		for i, item := range d.tasks {
			if item == t {
				d.tasks = append(d.tasks[:i], d.tasks[i+1:]...)
				break
			}
		}
	}
	d.mu.Unlock()
	d.a.redraw()
	return t
}

func (d *Downloader) removeFiles(t *DownloadTask) {
	d.mu.Lock()
	path, lrc := t.Path, t.lrcPath
	d.mu.Unlock()
	for _, name := range []string{path, lrc} {
		if name != "" {
			_ = os.Remove(name)
		}
	}
}

// Delete 移除已完成的任务并删除下载的文件；队列中播放该文件的条目一并移出。
func (d *Downloader) Delete(id int) {
	d.mu.Lock()
	t := d.find(id)
	done := t != nil && t.Status == DownloadSuccess
	d.mu.Unlock()
	if !done {
		return
	}
	d.Remove(id)
	for i, queued := range d.a.Snapshot().Queue {
		if strings.EqualFold(queued.ID, t.Path) {
			d.a.RemoveFromQueue(i)
			break
		}
	}
	d.removeFiles(t)
}

// Path 返回已完成任务的文件路径。
func (d *Downloader) Path(id int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.find(id); t != nil && t.Status == DownloadSuccess {
		return t.Path
	}
	return ""
}
