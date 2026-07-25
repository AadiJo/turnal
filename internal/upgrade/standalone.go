package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const defaultReleaseBaseURL = "https://github.com/AadiJo/turnal/releases/download"
const defaultStandaloneDownloadTimeout = 5 * time.Minute

var standaloneExecutables = []string{
	"turnal",
	"turnal-adapter-opencode",
	"turnal-adapter-gemini-cli",
	"turnal-adapter-copilot-cli",
}

type StandaloneInstallOptions struct {
	Version        string
	Channel        string
	InstallDir     string
	ExecutablePath string
	ReleaseBaseURL string
	Client         *http.Client
}

func InstallStandalone(ctx context.Context, opts StandaloneInstallOptions) error {
	version := strings.TrimPrefix(strings.TrimSpace(opts.Version), "v")
	if _, err := parseVersion(version); err != nil {
		return fmt.Errorf("standalone upgrade version: %w", err)
	}
	if opts.Channel != ChannelStable && opts.Channel != ChannelNightly {
		return fmt.Errorf("standalone upgrade channel %q is unsupported", opts.Channel)
	}
	platform, err := standalonePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	installDir, err := resolveInstallDir(opts)
	if err != nil {
		return err
	}

	baseURL := strings.TrimRight(opts.ReleaseBaseURL, "/")
	if baseURL == "" {
		baseURL = defaultReleaseBaseURL
	}
	archiveName := fmt.Sprintf("turnal_%s_%s.tar.gz", version, platform)
	releaseURL := baseURL + "/v" + version
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: defaultStandaloneDownloadTimeout}
	}
	archive, err := download(ctx, client, releaseURL+"/"+archiveName, 256<<20)
	if err != nil {
		return fmt.Errorf("download %s: %w", archiveName, err)
	}
	checksums, err := download(ctx, client, releaseURL+"/checksums.txt", 1<<20)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	if err := verifyArchiveChecksum(archiveName, archive, string(checksums)); err != nil {
		return err
	}

	stageDir, err := os.MkdirTemp(installDir, ".turnal-upgrade-stage-*")
	if err != nil {
		return fmt.Errorf("create standalone upgrade staging directory in %s: %w", installDir, err)
	}
	defer os.RemoveAll(stageDir)
	if err := extractStandaloneArchive(archive, stageDir); err != nil {
		return err
	}
	if err := verifyStagedStandalone(ctx, filepath.Join(stageDir, "turnal"), version, opts.Channel); err != nil {
		return err
	}
	if err := replaceStandaloneFiles(stageDir, installDir); err != nil {
		return err
	}
	return nil
}

func standalonePlatform(goos, goarch string) (string, error) {
	var platform string
	switch goos {
	case "darwin", "linux":
		platform = goos
	default:
		return "", fmt.Errorf("standalone upgrades are unsupported on %s", goos)
	}
	var architecture string
	switch goarch {
	case "amd64", "arm64":
		architecture = goarch
	default:
		return "", fmt.Errorf("standalone upgrades are unsupported on %s/%s", goos, goarch)
	}
	return platform + "_" + architecture, nil
}

func resolveInstallDir(opts StandaloneInstallOptions) (string, error) {
	if strings.TrimSpace(opts.InstallDir) != "" {
		return filepath.Abs(opts.InstallDir)
	}
	executablePath := opts.ExecutablePath
	if executablePath == "" {
		var err error
		executablePath, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve current executable: %w", err)
		}
	}
	executablePath, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		return "", fmt.Errorf("resolve current executable path: %w", err)
	}
	return filepath.Dir(executablePath), nil
}

func download(ctx context.Context, client *http.Client, url string, maxBytes int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("release server returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func verifyArchiveChecksum(name string, archive []byte, checksums string) error {
	var expected string
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			expected = fields[0]
			break
		}
	}
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("checksums.txt does not contain a valid checksum for %s", name)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("checksums.txt contains an invalid checksum for %s", name)
	}
	actual := sha256.Sum256(archive)
	if !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
		return fmt.Errorf("checksum verification failed for %s", name)
	}
	return nil
}

func extractStandaloneArchive(archive []byte, destination string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open standalone archive: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	expected := make(map[string]bool, len(standaloneExecutables))
	for _, name := range standaloneExecutables {
		expected[name] = false
	}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read standalone archive: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(header.Name), "./")
		if _, ok := expected[name]; !ok || header.Typeflag != tar.TypeReg {
			return fmt.Errorf("standalone archive contains unexpected entry %q", header.Name)
		}
		if expected[name] {
			return fmt.Errorf("standalone archive contains duplicate entry %q", name)
		}
		if header.Size < 0 || header.Size > 128<<20 {
			return fmt.Errorf("standalone archive entry %q has invalid size %d", name, header.Size)
		}
		target := filepath.Join(destination, name)
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("create staged %s: %w", name, err)
		}
		_, copyErr := io.Copy(file, io.LimitReader(reader, 128<<20))
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("extract staged %s: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close staged %s: %w", name, closeErr)
		}
		if err := os.Chmod(target, 0o755); err != nil {
			return fmt.Errorf("set staged %s executable mode: %w", name, err)
		}
		expected[name] = true
	}
	for name, found := range expected {
		if !found {
			return fmt.Errorf("standalone archive is missing %s", name)
		}
	}
	return nil
}

func verifyStagedStandalone(ctx context.Context, executable, version, channel string) error {
	output, err := exec.CommandContext(ctx, executable, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("verify staged turnal executable: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return fmt.Errorf("parse staged turnal metadata: %w", err)
	}
	if metadata.Version != version || metadata.Channel != channel || metadata.InstallSource != InstallSourceStandalone {
		return fmt.Errorf(
			"staged turnal metadata mismatch: got version=%q channel=%q install_source=%q",
			metadata.Version,
			metadata.Channel,
			metadata.InstallSource,
		)
	}
	return nil
}

func replaceStandaloneFiles(stageDir, installDir string) error {
	return replaceStandaloneFilesWithRename(stageDir, installDir, os.Rename)
}

func replaceStandaloneFilesWithRename(stageDir, installDir string, rename func(string, string) error) error {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create standalone install directory: %w", err)
	}
	transactionDir, err := os.MkdirTemp(installDir, ".turnal-upgrade-*")
	if err != nil {
		return fmt.Errorf("prepare standalone upgrade in %s: %w", installDir, err)
	}
	defer os.RemoveAll(transactionDir)

	for _, name := range standaloneExecutables {
		if err := copyExecutable(filepath.Join(stageDir, name), filepath.Join(transactionDir, name+".new")); err != nil {
			return err
		}
	}

	replaced := make([]string, 0, len(standaloneExecutables))
	hadOriginal := make(map[string]bool, len(standaloneExecutables))
	rollback := func() {
		for index := len(replaced) - 1; index >= 0; index-- {
			name := replaced[index]
			target := filepath.Join(installDir, name)
			_ = os.Remove(target)
			if hadOriginal[name] {
				_ = rename(filepath.Join(transactionDir, name+".old"), target)
			}
		}
	}

	for _, name := range standaloneExecutables {
		target := filepath.Join(installDir, name)
		backup := filepath.Join(transactionDir, name+".old")
		if _, err := os.Lstat(target); err == nil {
			if err := rename(target, backup); err != nil {
				rollback()
				return fmt.Errorf("back up installed %s: %w", name, err)
			}
			hadOriginal[name] = true
		} else if !os.IsNotExist(err) {
			rollback()
			return fmt.Errorf("inspect installed %s: %w", name, err)
		}
		if err := rename(filepath.Join(transactionDir, name+".new"), target); err != nil {
			if hadOriginal[name] {
				_ = rename(backup, target)
			}
			rollback()
			return fmt.Errorf("install %s: %w", name, err)
		}
		replaced = append(replaced, name)
	}
	return nil
}

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open staged executable: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("create replacement executable: %w", err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("copy replacement executable: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close replacement executable: %w", closeErr)
	}
	if err := os.Chmod(destination, 0o755); err != nil {
		return fmt.Errorf("set replacement executable mode: %w", err)
	}
	return nil
}
