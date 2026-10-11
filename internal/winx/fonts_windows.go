package winx

import (
	"errors"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procEnumFontFamiliesEx = gdi32.NewProc("EnumFontFamiliesExW")
	procCreateFontIndirect = gdi32.NewProc("CreateFontIndirectW")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procGetFontData        = gdi32.NewProc("GetFontData")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
)

// DefaultFont is the interface font when the user has not chosen one.
const DefaultFont = "Microsoft YaHei"

type logFont struct {
	Height, Width, Escapement, Orientation, Weight int32
	Italic, Underline, StrikeOut, CharSet          byte
	OutPrecision, ClipPrecision, Quality, Pitch    byte
	FaceName                                       [32]uint16
}

// Fonts lists the font families installed on this machine, by the names the system shows.
func Fonts() []string {
	dc, _, _ := procCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil
	}
	defer procDeleteDC.Call(dc)
	seen := map[string]bool{}
	callback := syscall.NewCallback(func(font *logFont, metric, kind, param uintptr) uintptr {
		name := windows.UTF16ToString(font.FaceName[:])
		// Names beginning with @ are the vertical-writing twins of East Asian fonts.
		// Only outline fonts (TRUETYPE_FONTTYPE): the old bitmap fonts cannot draw an interface.
		if name != "" && !strings.HasPrefix(name, "@") && kind&4 != 0 {
			seen[name] = true
		}
		return 1
	})
	query := logFont{CharSet: 1} // DEFAULT_CHARSET: every family, in every script
	procEnumFontFamiliesEx.Call(dc, uintptr(unsafe.Pointer(&query)), callback, 0, 0)
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names
}

// FontData returns the font file of an installed family (a collection when it lives in one),
// for code that draws text itself. An unknown family yields what the system substitutes for it.
func FontData(family string) ([]byte, error) {
	if family == "" {
		family = DefaultFont
	}
	dc, _, _ := procCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil, errors.New("无法读取系统字体")
	}
	defer procDeleteDC.Call(dc)
	font := logFont{CharSet: 1, Weight: 400}
	copy(font.FaceName[:31], windows.StringToUTF16(family))
	handle, _, _ := procCreateFontIndirect.Call(uintptr(unsafe.Pointer(&font)))
	if handle == 0 {
		return nil, errors.New("无法读取系统字体")
	}
	defer procDeleteObject.Call(handle)
	procSelectObject.Call(dc, handle)
	// 'ttcf' asks for the whole collection file; a plain font file is table 0.
	for _, table := range []uintptr{0x66637474, 0} {
		size, _, _ := procGetFontData.Call(dc, table, 0, 0, 0)
		if int32(size) <= 0 {
			continue
		}
		data := make([]byte, size)
		if got, _, _ := procGetFontData.Call(dc, table, 0, uintptr(unsafe.Pointer(&data[0])), size); got == size {
			return data, nil
		}
	}
	return nil, errors.New("无法读取字体文件：" + family)
}
