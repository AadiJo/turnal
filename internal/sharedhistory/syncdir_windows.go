//go:build windows

package sharedhistory

// replaceFile uses MOVEFILE_WRITE_THROUGH; Windows does not expose the same
// directory fsync operation used by Unix.
func syncDirectory(string) error { return nil }
