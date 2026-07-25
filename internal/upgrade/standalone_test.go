package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallStandaloneDownloadsVerifiesAndReplacesExecutables(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("standalone upgrades are only supported on macOS and Linux")
	}
	platform, err := standalonePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}

	version := "0.4.2"
	archiveName := fmt.Sprintf("turnal_%s_%s.tar.gz", version, platform)
	files := map[string][]byte{}
	for _, name := range standaloneExecutables {
		files[name] = []byte("#!/bin/sh\necho replacement-" + name + "\n")
	}
	files["turnal"] = []byte("#!/bin/sh\nprintf '%s\\n' '{\"version\":\"0.4.2\",\"channel\":\"stable\",\"install_source\":\"standalone\"}'\n")
	archive := standaloneTestArchive(t, files)
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%x  %s\n", sum, archiveName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(archive)
		case "checksums.txt":
			_, _ = w.Write([]byte(checksums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	installDir := t.TempDir()
	for _, name := range standaloneExecutables {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte("old-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := InstallStandalone(context.Background(), StandaloneInstallOptions{
		Version:        version,
		Channel:        ChannelStable,
		InstallDir:     installDir,
		ReleaseBaseURL: server.URL,
		Client:         server.Client(),
	}); err != nil {
		t.Fatalf("InstallStandalone: %v", err)
	}

	for _, name := range standaloneExecutables {
		data, err := os.ReadFile(filepath.Join(installDir, name))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if bytes.HasPrefix(data, []byte("old-")) {
			t.Fatalf("%s was not replaced", name)
		}
	}
}

func TestVerifyArchiveChecksumRejectsMismatch(t *testing.T) {
	err := verifyArchiveChecksum("turnal_1.0.0_linux_amd64.tar.gz", []byte("archive"), "0000000000000000000000000000000000000000000000000000000000000000  turnal_1.0.0_linux_amd64.tar.gz\n")
	if err == nil {
		t.Fatal("verifyArchiveChecksum succeeded, want mismatch")
	}
}

func TestExtractStandaloneArchiveRejectsUnexpectedEntry(t *testing.T) {
	archive := standaloneTestArchive(t, map[string][]byte{"unexpected": []byte("payload")})
	err := extractStandaloneArchive(archive, t.TempDir())
	if err == nil {
		t.Fatal("extractStandaloneArchive succeeded, want unexpected entry error")
	}
}

func standaloneTestArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, data := range files {
		header := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(data)),
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
