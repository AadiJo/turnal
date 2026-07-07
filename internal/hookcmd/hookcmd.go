package hookcmd

import (
	"os"
	"path/filepath"
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
	switch filepath.Base(executable) {
	case "turnal":
	default:
		return "turnal"
	}
	if isUnderTempDir(executable) {
		return "turnal"
	}
	return shellQuote(executable)
}

func isUnderTempDir(path string) bool {
	rel, err := filepath.Rel(os.TempDir(), path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`") {
		return value
	}
	return strconv.Quote(value)
}
