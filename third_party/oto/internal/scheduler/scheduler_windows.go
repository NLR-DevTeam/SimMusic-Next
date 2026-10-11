// Copyright 2026 SimMusic contributors. Licensed under the Apache License, Version 2.0.
package scheduler

import (
	"golang.org/x/sys/windows"
	"log"
	"runtime"
	"unsafe"
)

// Registration belongs to this OS thread, not to the process or a Go P.
func Begin(label string) func() {
	runtime.LockOSThread()
	avrt := windows.NewLazySystemDLL("avrt.dll")
	set := avrt.NewProc("AvSetMmThreadCharacteristicsW")
	revert := avrt.NewProc("AvRevertMmThreadCharacteristics")
	priority := avrt.NewProc("AvSetMmThreadPriority")
	task, _ := windows.UTF16PtrFromString("Pro Audio")
	var index uint32
	handle, _, err := set.Call(uintptr(unsafe.Pointer(task)), uintptr(unsafe.Pointer(&index)))
	if handle == 0 {
		log.Printf("audio: %s MMCSS registration failed: %v", label, err)
		return runtime.UnlockOSThread
	}
	if ok, _, err := priority.Call(handle, 2); ok == 0 {
		log.Printf("audio: %s MMCSS priority failed: %v", label, err)
	}
	log.Printf("audio: %s thread registered with MMCSS", label)
	return func() { revert.Call(handle); runtime.UnlockOSThread() }
}
