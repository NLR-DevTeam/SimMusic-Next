package winx

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pickerMu sync.Mutex
var ole32 = windows.NewLazySystemDLL("ole32.dll")
var fileOpenDialog = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
var fileOpenInterface = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}

// Indices follow IFileOpenDialog, IShellItemArray and IShellItem in shobjidl.h.
func comCall(object uintptr, method int, args ...uintptr) uintptr {
	vtable := *(*uintptr)(unsafe.Pointer(object))
	address := *(*uintptr)(unsafe.Add(unsafe.Pointer(vtable), uintptr(method)*unsafe.Sizeof(uintptr(0))))
	result, _, _ := syscall.SyscallN(address, append([]uintptr{object}, args...)...)
	return result
}

func comError(result uintptr) error {
	if int32(result) >= 0 {
		return nil
	}
	if uint32(result) == 0x800704c7 {
		return errors.New("已取消选择")
	}
	return fmt.Errorf("Windows 文件选择器: HRESULT 0x%08x", uint32(result))
}

// PickPaths uses the Explorer Common Item Dialog, owned by the CEF main window.
// The STA thread stays locked for the entire native modal loop and COM lifetime.
func PickPaths(owner uintptr, title string, folders bool, extensions []string) ([]string, error) {
	pickerMu.Lock()
	defer pickerMu.Unlock()
	valid, _, _ := user32.NewProc("IsWindow").Call(owner)
	if valid == 0 {
		return nil, errors.New("主窗口尚未准备好")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 6) // STA | DISABLE_OLE1DDE
	if err := comError(result); err != nil {
		return nil, err
	}
	defer windows.CoUninitialize()
	var dialog uintptr
	result, _, _ = ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&fileOpenDialog)), 0, 1, uintptr(unsafe.Pointer(&fileOpenInterface)), uintptr(unsafe.Pointer(&dialog)))
	if err := comError(result); err != nil {
		return nil, err
	}
	defer comCall(dialog, 2) // Release
	var options uint32
	if err := comError(comCall(dialog, 10, uintptr(unsafe.Pointer(&options)))); err != nil {
		return nil, err
	}
	options |= 0x40 | 0x800 // FORCEFILESYSTEM | PATHMUSTEXIST
	if folders {
		options |= 0x20 // PICKFOLDERS
	} else {
		options |= 0x200 | 0x1000 // ALLOWMULTISELECT | FILEMUSTEXIST
	}
	if err := comError(comCall(dialog, 9, uintptr(options))); err != nil {
		return nil, err
	}
	titleText, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return nil, err
	}
	result = comCall(dialog, 17, uintptr(unsafe.Pointer(titleText))) // SetTitle
	runtime.KeepAlive(titleText)
	if err := comError(result); err != nil {
		return nil, err
	}
	if !folders && len(extensions) > 0 {
		patterns := make([]string, len(extensions))
		for index, extension := range extensions {
			patterns[index] = "*." + strings.TrimPrefix(extension, ".")
		}
		name, _ := windows.UTF16PtrFromString("支持的文件")
		pattern, err := windows.UTF16PtrFromString(strings.Join(patterns, ";"))
		if err != nil {
			return nil, err
		}
		filter := struct{ Name, Pattern *uint16 }{name, pattern}
		result = comCall(dialog, 4, 1, uintptr(unsafe.Pointer(&filter))) // SetFileTypes
		runtime.KeepAlive(filter)
		if err := comError(result); err != nil {
			return nil, err
		}
	}
	if err := comError(comCall(dialog, 3, owner)); err != nil { // Show(HWND owner)
		return nil, err
	}
	var items uintptr
	if err := comError(comCall(dialog, 27, uintptr(unsafe.Pointer(&items)))); err != nil { // GetResults
		return nil, err
	}
	defer comCall(items, 2)
	var count uint32
	if err := comError(comCall(items, 7, uintptr(unsafe.Pointer(&count)))); err != nil { // GetCount
		return nil, err
	}
	paths := make([]string, 0, count)
	for index := uint32(0); index < count; index++ {
		var item uintptr
		if err := comError(comCall(items, 8, uintptr(index), uintptr(unsafe.Pointer(&item)))); err != nil {
			return nil, err
		}
		var path *uint16
		result = comCall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&path))) // GetDisplayName(SIGDN_FILESYSPATH)
		comCall(item, 2)
		if err := comError(result); err != nil {
			return nil, err
		}
		paths = append(paths, windows.UTF16PtrToString(path))
		windows.CoTaskMemFree(unsafe.Pointer(path))
	}
	return paths, nil
}
