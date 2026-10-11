package winx

import (
	"errors"
	"golang.org/x/sys/windows"
	"unsafe"
)

func CopyText(text string) error {
	value, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	opened, _, _ := user32.NewProc("OpenClipboard").Call(0)
	if opened == 0 {
		return errors.New("剪贴板暂时不可用")
	}
	defer user32.NewProc("CloseClipboard").Call()
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	memory, _, _ := kernel.NewProc("GlobalAlloc").Call(2, uintptr(len(value)*2))
	if memory == 0 {
		return errors.New("无法分配剪贴板内存")
	}
	owned := true
	defer func() {
		if owned {
			kernel.NewProc("GlobalFree").Call(memory)
		}
	}()
	pointer, _, _ := kernel.NewProc("GlobalLock").Call(memory)
	if pointer == 0 {
		return errors.New("无法写入剪贴板内存")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(pointer)), len(value)), value)
	kernel.NewProc("GlobalUnlock").Call(memory)
	cleared, _, _ := user32.NewProc("EmptyClipboard").Call()
	if cleared == 0 {
		return errors.New("无法清空剪贴板")
	}
	result, _, _ := user32.NewProc("SetClipboardData").Call(13, memory)
	if result == 0 {
		return errors.New("无法设置剪贴板")
	}
	owned = false
	return nil
}
