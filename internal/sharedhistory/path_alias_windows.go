//go:build windows

package sharedhistory

import "golang.org/x/sys/windows"

func platformWorkspaceRootAliases(root string) []string {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil
	}

	var aliases []string
	for _, resolve := range []func(*uint16, *uint16, uint32) (uint32, error){windows.GetLongPathName, windows.GetShortPathName} {
		buffer := make([]uint16, 32768)
		length, err := resolve(path, &buffer[0], uint32(len(buffer)))
		if err == nil && length > 0 && length < uint32(len(buffer)) {
			aliases = append(aliases, windows.UTF16ToString(buffer[:length]))
		}
	}
	return aliases
}
