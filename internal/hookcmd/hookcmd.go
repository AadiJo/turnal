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
		return "agent-vcs"
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "agent-vcs"
	}
	switch filepath.Base(executable) {
	case "agent-vcs", "acs":
	default:
		return "agent-vcs"
	}
	if isUnderTempDir(executable) {
		return "agent-vcs"
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
