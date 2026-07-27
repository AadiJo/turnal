package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		files[name] = standaloneMetadataScript(version, ChannelStable, InstallSourceStandalone)
	}
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

func TestVerifyArchiveChecksumRejectsMissingEntry(t *testing.T) {
	err := verifyArchiveChecksum("turnal_1.0.0_linux_amd64.tar.gz", []byte("archive"), "")
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("verifyArchiveChecksum error = %v, want missing entry", err)
	}
}

func TestDownloadReportsNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := download(context.Background(), server.Client(), server.URL+"/missing", 1024)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("download error = %v, want 404", err)
	}
}

func TestExtractStandaloneArchiveRejectsUnexpectedEntry(t *testing.T) {
	archive := standaloneTestArchive(t, map[string][]byte{"unexpected": []byte("payload")})
	err := extractStandaloneArchive(archive, t.TempDir())
	if err == nil {
		t.Fatal("extractStandaloneArchive succeeded, want unexpected entry error")
	}
}

func TestExtractStandaloneArchiveRejectsPathTraversal(t *testing.T) {
	archive := standaloneTestArchiveEntries(t, []standaloneTestArchiveEntry{{
		header: tar.Header{
			Name: "../turnal",
			Mode: 0o755,
		},
		data: []byte("payload"),
	}})
	destination := t.TempDir()
	err := extractStandaloneArchive(archive, destination)
	if err == nil || !strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("extractStandaloneArchive error = %v, want unexpected entry", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "..", "turnal")); !os.IsNotExist(statErr) {
		t.Fatalf("path traversal created file outside destination: %v", statErr)
	}
}

func TestExtractStandaloneArchiveRejectsSymlink(t *testing.T) {
	archive := standaloneTestArchiveEntries(t, []standaloneTestArchiveEntry{{
		header: tar.Header{
			Name:     "turnal",
			Mode:     0o755,
			Typeflag: tar.TypeSymlink,
			Linkname: "../victim",
		},
	}})
	err := extractStandaloneArchive(archive, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("extractStandaloneArchive error = %v, want unexpected entry", err)
	}
}

func TestReplaceStandaloneFilesRollsBackMidTransactionFailure(t *testing.T) {
	stageDir := t.TempDir()
	installDir := t.TempDir()
	for _, name := range standaloneExecutables {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte("new-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(installDir, name), []byte("old-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rename := func(source, destination string) error {
		if strings.HasSuffix(source, "turnal-adapter-gemini-cli.new") {
			return errors.New("injected replacement failure")
		}
		return os.Rename(source, destination)
	}
	err := replaceStandaloneFilesWithRename(stageDir, installDir, rename)
	if err == nil || !strings.Contains(err.Error(), "injected replacement failure") {
		t.Fatalf("replace error = %v", err)
	}
	for _, name := range standaloneExecutables {
		data, readErr := os.ReadFile(filepath.Join(installDir, name))
		if readErr != nil {
			t.Fatalf("read restored %s: %v", name, readErr)
		}
		if string(data) != "old-"+name {
			t.Fatalf("%s = %q, want original", name, data)
		}
	}
}

func TestVerifyStagedStandaloneRejectsMetadataMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell script")
	}
	stageDir := t.TempDir()
	for _, name := range standaloneExecutables {
		body := standaloneMetadataScript("1.2.3", ChannelStable, InstallSourceStandalone)
		if name == "turnal-adapter-opencode" {
			body = standaloneMetadataScript("9.9.9", ChannelStable, InstallSourceStandalone)
		}
		if err := os.WriteFile(filepath.Join(stageDir, name), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	err := verifyStagedStandalone(context.Background(), stageDir, "1.2.3", ChannelStable)
	if err == nil || !strings.Contains(err.Error(), "turnal-adapter-opencode metadata mismatch") {
		t.Fatalf("verify error = %v, want adapter metadata mismatch", err)
	}
}

func standaloneMetadataScript(version, channel, installSource string) []byte {
	return []byte(fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' '{\"version\":%q,\"channel\":%q,\"install_source\":%q}'\n",
		version,
		channel,
		installSource,
	))
}

func standaloneTestArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	entries := make([]standaloneTestArchiveEntry, 0, len(files))
	for name, data := range files {
		entries = append(entries, standaloneTestArchiveEntry{
			header: tar.Header{
				Name: name,
				Mode: 0o755,
			},
			data: data,
		})
	}
	return standaloneTestArchiveEntries(t, entries)
}

type standaloneTestArchiveEntry struct {
	header tar.Header
	data   []byte
}

func standaloneTestArchiveEntries(t *testing.T, entries []standaloneTestArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		entry.header.Size = int64(len(entry.data))
		if err := tarWriter.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
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
