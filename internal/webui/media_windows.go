package webui

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The CEF host owns the main window and with it the taskbar preview buttons and the System Media
// Transport Controls. The core tells it what is playing; it reports pressed controls as "MEDIA:" lines.

type copyData struct {
	kind uintptr
	size uint32
	data unsafe.Pointer
}

var procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

func sendMediaState(hwnd uintptr, text string) bool { return sendHost(hwnd, 1, text) }

// launchFromHost has the host, the process that owns the window the user is working in, open a
// folder, select a file or open an address: only the foreground process may bring the new window
// to the front. It reports false when there is no host window to ask.
func (u *UI) launchFromHost(kind, target string) bool {
	hwnd := uintptr(u.hwnd.Load())
	return hwnd != 0 && sendHost(hwnd, 2, kind+"\t"+target)
}

func sendHost(hwnd, kind uintptr, text string) bool {
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return false
	}
	message := copyData{kind: kind, size: uint32(len(utf16) * 2), data: unsafe.Pointer(&utf16[0])}
	var result uintptr
	const wmCopyData, abortIfHung = 0x004A, 0x0002
	sent, _, _ := procSendMessageTimeout.Call(hwnd, wmCopyData, 0, uintptr(unsafe.Pointer(&message)), abortIfHung, 1000, uintptr(unsafe.Pointer(&result)))
	return sent != 0 && result != 0
}

// syncMedia sends "<state>\t<title>\t<artist>\t<cover address>" whenever it changes: state 0 is
// nothing to play, 1 paused, 2 playing. The cover is fetched from this process's own cover endpoint.
func (u *UI) syncMedia() {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	field := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")
	last := ""
	for {
		select {
		case <-u.quit:
			return
		case <-tick.C:
		}
		hwnd := uintptr(u.hwnd.Load())
		if hwnd == 0 {
			continue
		}
		s := u.app.Snapshot()
		text := "0\t\t\t"
		if t := s.Current; t != nil {
			state := "1"
			if s.Playing {
				state = "2"
			}
			artist := t.Artist
			if artist == "未知艺术家" {
				artist = ""
			}
			cover := ""
			if t.InfoReady || t.Cover != "" {
				u.remember(s.Queue)
				cover = u.url + "cover?key=" + url.QueryEscape(t.Key()) + "&scene=play&rev=" + strconv.FormatUint(t.InfoRevision, 10)
			}
			text = strings.Join([]string{state, field.Replace(t.Title), field.Replace(artist), cover}, "\t")
		}
		if text != last && sendMediaState(hwnd, text) {
			last = text
		}
	}
}

// mediaAction handles a control pressed on the taskbar preview or the system media controls.
func (u *UI) mediaAction(action string) {
	a := u.app
	switch action {
	case "previous":
		a.Prev()
	case "next":
		a.Next(false)
	case "toggle":
		a.TogglePlay()
	case "play":
		if !a.Snapshot().Playing {
			a.TogglePlay()
		}
	case "pause", "stop":
		if a.Snapshot().Playing {
			a.TogglePlay()
		}
	}
}
