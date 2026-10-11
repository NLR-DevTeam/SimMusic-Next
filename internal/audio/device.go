package audio

import "github.com/ebitengine/oto/v3"

// OutputDevice 是一个可用的音频输出设备。
type OutputDevice = oto.OutputDevice

// OutputDevices 列出当前可用的输出设备。
func OutputDevices() ([]OutputDevice, error) { return oto.OutputDevices() }

// SetOutputDevice 选择输出设备；id 为空表示跟随系统默认设备。所选设备不可用时暂时使用系统默认设备。
func SetOutputDevice(id string) { oto.SetOutputDevice(id) }
