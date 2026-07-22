package agentskills

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// createDirectoryLink prefers a true directory symlink. Standard Windows
// installations may reject that operation unless Developer Mode is enabled or
// the process is elevated, so privilege failures fall back to an NTFS junction.
func createDirectoryLink(source string, target string, destination string) error {
	if err := os.Symlink(target, destination); err == nil {
		return nil
	} else if !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return err
	}
	return createJunction(source, destination)
}

func createJunction(source, destination string) (returnErr error) {
	absoluteSource, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve junction target %s: %w", source, err)
	}
	volume := filepath.VolumeName(absoluteSource)
	if len(volume) < 2 || volume[1] != ':' {
		return fmt.Errorf("junction fallback requires a local Windows drive target, got %s", absoluteSource)
	}

	substituteName, err := windows.UTF16FromString(`\??\` + absoluteSource)
	if err != nil {
		return fmt.Errorf("encode junction target: %w", err)
	}
	printName, err := windows.UTF16FromString(absoluteSource)
	if err != nil {
		return fmt.Errorf("encode junction display target: %w", err)
	}
	substituteBytes := (len(substituteName) - 1) * 2
	printBytes := (len(printName) - 1) * 2
	pathBytes := len(substituteName)*2 + len(printName)*2
	reparseDataLength := 8 + pathBytes
	if reparseDataLength > math.MaxUint16 || 8+reparseDataLength > windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE {
		return fmt.Errorf("junction target is too long: %s", absoluteSource)
	}

	buffer := make([]byte, 8+reparseDataLength)
	binary.LittleEndian.PutUint32(buffer[0:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:6], uint16(reparseDataLength))
	binary.LittleEndian.PutUint16(buffer[8:10], 0)
	binary.LittleEndian.PutUint16(buffer[10:12], uint16(substituteBytes))
	binary.LittleEndian.PutUint16(buffer[12:14], uint16(len(substituteName)*2))
	binary.LittleEndian.PutUint16(buffer[14:16], uint16(printBytes))
	writeUTF16(buffer[16:], substituteName)
	writeUTF16(buffer[16+len(substituteName)*2:], printName)

	if err := os.Mkdir(destination, 0o755); err != nil {
		return fmt.Errorf("create junction directory %s: %w", destination, err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.Remove(destination)
		}
	}()

	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return fmt.Errorf("encode junction path: %w", err)
	}
	handle, err := windows.CreateFile(
		destinationPointer,
		windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open junction directory %s: %w", destination, err)
	}
	defer windows.CloseHandle(handle)

	var bytesReturned uint32
	if err := windows.DeviceIoControl(
		handle,
		windows.FSCTL_SET_REPARSE_POINT,
		&buffer[0],
		uint32(len(buffer)),
		nil,
		0,
		&bytesReturned,
		nil,
	); err != nil {
		return fmt.Errorf("set junction reparse point: %w", err)
	}
	return nil
}

func writeUTF16(destination []byte, value []uint16) {
	for index, codeUnit := range value {
		binary.LittleEndian.PutUint16(destination[index*2:], codeUnit)
	}
}
