package viewer

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

// The picker reports three distinct outcomes, and the viewer treats each
// differently: a path, an ordinary cancellation, and a machine with no dialog
// available. Confusing the last two would either hide a real problem or turn
// pressing Escape into an error.
func TestPickerErrorsAreDistinguishable(t *testing.T) {
	var cancelled error = ErrPickerCancelled{}
	var unavailable error = ErrPickerUnavailable{Reason: "no dialog helper installed"}

	var asCancelled ErrPickerCancelled
	if !errors.As(cancelled, &asCancelled) {
		t.Fatal("cancellation does not match ErrPickerCancelled")
	}
	if errors.As(cancelled, &ErrPickerUnavailable{}) {
		t.Fatal("cancellation also matched ErrPickerUnavailable")
	}

	var asUnavailable ErrPickerUnavailable
	if !errors.As(unavailable, &asUnavailable) {
		t.Fatal("unavailable does not match ErrPickerUnavailable")
	}
	// The reason is shown to the user, so it has to survive wrapping.
	if !strings.Contains(asUnavailable.Error(), "no dialog helper installed") {
		t.Fatalf("unavailable message lost its reason: %q", asUnavailable.Error())
	}
}

// lastLine exists because some shells print banners before the value, so only
// the final non-empty line is the answer.
func TestLastLineTakesTheFinalValue(t *testing.T) {
	for _, testCase := range []struct{ in, want string }{
		{"C:\\Users\\you\\project", "C:\\Users\\you\\project"},
		{"banner\r\nC:\\picked\r\n", "C:\\picked"},
		{"\n\n/home/you/project\n\n", "/home/you/project"},
		{"", ""},
		{"   \n  \n", ""},
	} {
		if got := lastLine(testCase.in); got != testCase.want {
			t.Fatalf("lastLine(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

// The dialog opens at a directory, and the helpers that take a filename need a
// trailing separator to treat the value as one.
func TestEnsureTrailingSeparator(t *testing.T) {
	separator := string(os.PathSeparator)
	if got := ensureTrailingSeparator("/home/you"); got != "/home/you"+separator {
		t.Fatalf("missing separator was not added: %q", got)
	}
	if got := ensureTrailingSeparator("/home/you" + separator); got != "/home/you"+separator {
		t.Fatalf("separator was duplicated: %q", got)
	}
	if got := ensureTrailingSeparator(""); got != "" {
		t.Fatalf("empty path gained a separator: %q", got)
	}
}

// On WSL the useful dialog is the Windows one, reached through interop. Interop
// can be enabled while the Windows directories are absent from PATH, so
// resolution must not depend on PATH alone.
func TestFindWindowsPowerShellOnWSL(t *testing.T) {
	if runtime.GOOS != "linux" || !insideWSL() {
		t.Skip("not running under WSL")
	}
	path, err := findWindowsPowerShell()
	if err != nil {
		t.Skipf("WSL interop is not available in this environment: %v", err)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
		t.Fatalf("resolved powershell path is not a file: %q (%v)", path, statErr)
	}
}

func TestDefaultPickerStartIsAbsolute(t *testing.T) {
	start := defaultPickerStart()
	if start == "" {
		t.Skip("no working directory or home directory is available")
	}
	if !strings.HasPrefix(start, string(os.PathSeparator)) && runtime.GOOS != "windows" {
		t.Fatalf("picker start is not absolute: %q", start)
	}
}
