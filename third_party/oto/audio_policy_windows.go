// Copyright 2026 SimMusic contributors. Licensed under the Apache License, Version 2.0.
package oto

import (
	"fmt"
	"log"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var audioSessionControlID = windows.GUID{Data1: 0xf4b1a599, Data2: 0x7266, Data3: 0x4319, Data4: [8]byte{0xa8, 0xca, 0xe7, 0x0a, 0xcb, 0x11, 0xe8, 0xcd}}
var audioSessionControl2ID = windows.GUID{Data1: 0xbfb7ff88, Data2: 0x7239, Data3: 0x4fc9, Data4: [8]byte{0x8f, 0xa2, 0x07, 0xc9, 0x50, 0xbe, 0x9c, 0x6d}}

type audioSessionControl struct{ vtbl *[16]uintptr }

func (c *wasapiContext) disableDucking() error {
	p, err := c.client.GetService(&audioSessionControlID)
	if err != nil {
		return err
	}
	control := (*audioSessionControl)(p)
	defer syscall.SyscallN(control.vtbl[2], uintptr(p))
	var extended *audioSessionControl
	r, _, _ := syscall.SyscallN(control.vtbl[0], uintptr(p), uintptr(unsafe.Pointer(&audioSessionControl2ID)), uintptr(unsafe.Pointer(&extended)))
	if int32(r) < 0 {
		return fmt.Errorf("oto: audio session QueryInterface: HRESULT(%#x)", uint32(r))
	}
	defer syscall.SyscallN(extended.vtbl[2], uintptr(unsafe.Pointer(extended)))
	r, _, _ = syscall.SyscallN(extended.vtbl[15], uintptr(unsafe.Pointer(extended)), 1)
	if int32(r) < 0 {
		return fmt.Errorf("oto: SetDuckingPreference: HRESULT(%#x)", uint32(r))
	}
	log.Print("audio: WASAPI session automatic ducking disabled")
	return nil
}
