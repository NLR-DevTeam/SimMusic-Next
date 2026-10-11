package oto

import (
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// Exercises the COM plumbing against whatever endpoints the machine knows, present or not.
func TestOutputDeviceEnumeration(t *testing.T) {
	devices, err := outputDevices(0xF)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Skip("this machine knows no render endpoints")
	}
	for _, device := range devices {
		if device.ID == "" || device.Name == "" || device.Name == device.ID {
			t.Fatalf("endpoint without a readable name: %+v", device)
		}
		t.Logf("%s = %s", device.ID, device.Name)
	}
	active, err := OutputDevices()
	if err != nil {
		t.Fatal(err)
	}
	isActive := map[string]bool{}
	for _, device := range active {
		isActive[device.ID] = true
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	defer windows.CoUninitialize()
	e, err := _CoCreateInstance(&uuidMMDeviceEnumerator, nil, uint32(_CLSCTX_ALL), &uuidIMMDeviceEnumerator)
	if err != nil {
		t.Fatal(err)
	}
	enumerator := (*_IMMDeviceEnumerator)(e)
	defer enumerator.Release()
	for _, known := range devices {
		device, err := enumerator.getDevice(known.ID)
		if err != nil {
			t.Fatalf("a listed endpoint must be found by its id: %v", err)
		}
		if device.active() != isActive[known.ID] {
			t.Fatalf("%s: active() disagrees with the active list", known.Name)
		}
		device.Release()
	}
	if _, err := enumerator.getDevice("no-such-endpoint"); err == nil {
		t.Fatal("an unknown id must not resolve")
	}
}
