package hookcmd

import "testing"

func TestIsTurnalExecutableRecognizesWindowsBinary(t *testing.T) {
	for _, test := range []struct {
		base string
		goos string
		want bool
	}{
		{base: "turnal", goos: "linux", want: true},
		{base: "turnal", goos: "windows", want: true},
		{base: "turnal.exe", goos: "windows", want: true},
		{base: "TURNAL.EXE", goos: "windows", want: true},
		{base: "turnal.exe", goos: "linux", want: false},
		{base: "go-build.exe", goos: "windows", want: false},
	} {
		if got := isTurnalExecutable(test.base, test.goos); got != test.want {
			t.Errorf("isTurnalExecutable(%q, %q) = %v, want %v", test.base, test.goos, got, test.want)
		}
	}
}

func TestShellQuoteUsesWindowsPathSyntax(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "npm path without spaces",
			path: `C:\Users\Aadi\AppData\Roaming\npm\node_modules\@aadijo\turnal\turnal.exe`,
			want: `C:\Users\Aadi\AppData\Roaming\npm\node_modules\@aadijo\turnal\turnal.exe`,
		},
		{
			name: "program files path",
			path: `C:\Program Files\Turnal\turnal.exe`,
			want: `"C:\Program Files\Turnal\turnal.exe"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shellQuote(test.path, "windows"); got != test.want {
				t.Fatalf("shellQuote() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestShellQuotePreservesUnixBehavior(t *testing.T) {
	if got, want := shellQuote("/opt/Turnal CLI/turnal", "linux"), `"/opt/Turnal CLI/turnal"`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}
