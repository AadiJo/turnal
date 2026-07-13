package cli

import (
	"io"
	"os"
)

func colorOutputEnabled(w io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func stripANSIBytes(data []byte) []byte {
	clean := make([]byte, 0, len(data))
	for index := 0; index < len(data); {
		if data[index] == '\x1b' && index+1 < len(data) && data[index+1] == '[' {
			index += 2
			for index < len(data) {
				current := data[index]
				index++
				if current >= '@' && current <= '~' {
					break
				}
			}
			continue
		}
		clean = append(clean, data[index])
		index++
	}
	return clean
}
