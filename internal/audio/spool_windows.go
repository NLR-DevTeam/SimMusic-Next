package audio

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

var spoolSerial atomic.Uint64

// spoolFile opens the file an online song is kept in while it plays. The system deletes it when it is closed,
// however this program ends, and as a temporary file it stays in the system's cache rather than going to disk
// wherever it can.
func spoolFile() (*os.File, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("SimMusicNext-%d-%d.audio", os.Getpid(), spoolSerial.Add(1)))
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
