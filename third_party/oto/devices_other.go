//go:build !windows

// SimMusic addition to the vendored driver. Choosing the render endpoint is implemented for WASAPI only;
// elsewhere the driver plays through the system's default device, which the system's own sound settings
// (PulseAudio / PipeWire on Linux) direct to an output.

package oto

// OutputDevice is one audio output the user may choose.
type OutputDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// OutputDevices lists the outputs to choose from: none, the system decides.
func OutputDevices() ([]OutputDevice, error) { return nil, nil }

// SetOutputDevice has no effect: playback follows the system's default device.
func SetOutputDevice(id string) {}
