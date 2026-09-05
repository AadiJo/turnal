package adapters

import (
	"io/fs"
	"os"
)

// Reject special paths before opening them. Unix opens also use O_NONBLOCK so
// replacing a regular path with a FIFO cannot stall capture between Stat and
// OpenFile. Callers must still validate the opened file before reading it.
func openTranscriptFile(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open transcript", Path: path, Err: fs.ErrInvalid}
	}
	return os.OpenFile(path, transcriptReadFlags, 0)
}
