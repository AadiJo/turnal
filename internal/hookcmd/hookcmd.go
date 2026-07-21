package hookcmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func Default() string {
	executable, err := os.Executable()
	if err != nil {
		return "turnal"
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "turnal"
	}
	if !isTurnalExecutable(filepath.Base(executable), runtime.GOOS) {
		return "turnal"
	}
	if isUnderTempDir(executable) {
		return "turnal"
	}
	return shellQuote(executable, runtime.GOOS)
}

func isTurnalExecutable(base string, goos string) bool {
	if base == "turnal" {
		return true
	}
	return goos == "windows" && strings.EqualFold(base, "turnal.exe")
}

func isUnderTempDir(path string) bool {
	rel, err := filepath.Rel(os.TempDir(), path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func shellQuote(value string, goos string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`") {
		return value
	}
	if goos == "windows" {
		return `"` + value + `"`
	}
	return strconv.Quote(value)
}
