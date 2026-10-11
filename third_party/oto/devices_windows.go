// SimMusic addition to the vendored driver: choosing the render endpoint.

package oto

import (
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var outputDevice atomic.Value // string

// SetOutputDevice selects the render endpoint by its id; "" follows the system default. A running
// context notices at its next device check, half a second at most, and restarts on that endpoint.
// An endpoint that is missing or inactive falls back to the system default until it returns.
func SetOutputDevice(id string) { outputDevice.Store(id) }

func selectedOutputDevice() string {
	id, _ := outputDevice.Load().(string)
	return id
}

// OutputDevice is an active render endpoint.
type OutputDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const (
	deviceStateActive = 0x1
	stgmRead          = 0x0
	vtLPWSTR          = 31
)

var (
	pkeyDeviceFriendlyName = propertyKey{windows.GUID{Data1: 0xa45c254e, Data2: 0xdf1c, Data3: 0x4efd, Data4: [8]byte{0x80, 0x20, 0x67, 0xd1, 0x46, 0xa8, 0x50, 0xe0}}, 14}
	procPropVariantClear   = windows.NewLazySystemDLL("ole32.dll").NewProc("PropVariantClear")
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

type propVariant struct {
	vt       uint16
	reserved [3]uint16
	value    uintptr
	extra    uintptr
}

type comObject struct{ vtbl *[16]uintptr }

func (o *comObject) call(slot int, args ...uintptr) uint32 {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	return uint32(r)
}

func (o *comObject) release() { o.call(2) }

// getDevice is IMMDeviceEnumerator::GetDevice.
func (i *_IMMDeviceEnumerator) getDevice(id string) (*_IMMDevice, error) {
	name, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return nil, err
	}
	var device *_IMMDevice
	r, _, _ := syscall.SyscallN(i.vtbl.GetDevice, uintptr(unsafe.Pointer(i)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&device)))
	runtime.KeepAlive(name)
	if uint32(r) != uint32(windows.S_OK) || device == nil {
		return nil, fmt.Errorf("oto: IMMDeviceEnumerator::GetDevice failed: HRESULT(%d)", uint32(r))
	}
	return device, nil
}

func (i *_IMMDevice) active() bool {
	var state uint32
	r, _, _ := syscall.SyscallN(i.vtbl.GetState, uintptr(unsafe.Pointer(i)), uintptr(unsafe.Pointer(&state)))
	return uint32(r) == uint32(windows.S_OK) && state == deviceStateActive
}

func (i *_IMMDevice) friendlyName() string {
	var store *comObject
	r, _, _ := syscall.SyscallN(i.vtbl.OpenPropertyStore, uintptr(unsafe.Pointer(i)), stgmRead, uintptr(unsafe.Pointer(&store)))
	if uint32(r) != uint32(windows.S_OK) || store == nil {
		return ""
	}
	defer store.release()
	var value propVariant
	// IPropertyStore::GetValue
	if store.call(5, uintptr(unsafe.Pointer(&pkeyDeviceFriendlyName)), uintptr(unsafe.Pointer(&value))) != uint32(windows.S_OK) {
		return ""
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&value)))
	if value.vt != vtLPWSTR || value.value == 0 {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(value.value)))
}

// targetDevice is the endpoint the context should render to: the selected one while it is
// present and active, the system default otherwise.
func (c *wasapiContext) targetDevice() (*_IMMDevice, error) {
	if id := selectedOutputDevice(); id != "" {
		if device, err := c.enumerator.getDevice(id); err == nil {
			if device.active() {
				return device, nil
			}
			device.Release()
		}
	}
	return c.enumerator.GetDefaultAudioEndPoint(eRender, eConsole)
}

// OutputDevices lists the active render endpoints.
func OutputDevices() ([]OutputDevice, error) { return outputDevices(deviceStateActive) }

// outputDevices lists the render endpoints in any of the given states.
func outputDevices(states uint32) ([]OutputDevice, error) {
	type result struct {
		devices []OutputDevice
		err     error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && !errors.Is(err, syscall.Errno(windows.S_FALSE)) {
			done <- result{nil, err}
			return
		}
		defer windows.CoUninitialize()
		e, err := _CoCreateInstance(&uuidMMDeviceEnumerator, nil, uint32(_CLSCTX_ALL), &uuidIMMDeviceEnumerator)
		if err != nil {
			done <- result{nil, err}
			return
		}
		enumerator := (*_IMMDeviceEnumerator)(e)
		defer enumerator.Release()
		var collection *comObject
		r, _, _ := syscall.SyscallN(enumerator.vtbl.EnumAudioEndpoints, uintptr(unsafe.Pointer(enumerator)), uintptr(eRender), uintptr(states), uintptr(unsafe.Pointer(&collection)))
		if uint32(r) != uint32(windows.S_OK) || collection == nil {
			done <- result{nil, fmt.Errorf("oto: IMMDeviceEnumerator::EnumAudioEndpoints failed: HRESULT(%d)", uint32(r))}
			return
		}
		defer collection.release()
		var count uint32
		collection.call(3, uintptr(unsafe.Pointer(&count))) // IMMDeviceCollection::GetCount
		devices := []OutputDevice{}
		for index := uint32(0); index < count; index++ {
			var device *_IMMDevice
			if collection.call(4, uintptr(index), uintptr(unsafe.Pointer(&device))) != uint32(windows.S_OK) || device == nil { // Item
				continue
			}
			if id, err := device.GetId(); err == nil {
				name := device.friendlyName()
				if name == "" {
					name = id
				}
				devices = append(devices, OutputDevice{ID: id, Name: name})
			}
			device.Release()
		}
		done <- result{devices, nil}
	}()
	out := <-done
	return out.devices, out.err
}
