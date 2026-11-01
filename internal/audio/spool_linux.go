package audio

import "os"

// spoolFile opens the file an online song is kept in while it plays. It has no name from the start, so it
// goes when it is closed, however this program ends.
func spoolFile() (*os.File, error) {
	file, err := os.CreateTemp("", "SimMusicNext-*.audio")
	if err != nil {
		return nil, err
	}
	os.Remove(file.Name())
	return file, nil
}
