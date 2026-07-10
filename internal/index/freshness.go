package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func sourceFingerprint(metadataDir string) (string, error) {
	roots := []string{filepath.Join(metadataDir, "log", "events")}
	var records []string
	for _, root := range roots {
		info, err := os.Lstat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("fingerprint index source %s: %w", root, err)
		}
		if !info.IsDir() {
			records = append(records, fingerprintRecord(metadataDir, root, info))
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			records = append(records, fingerprintRecord(metadataDir, path, info))
			return nil
		}); err != nil {
			return "", fmt.Errorf("fingerprint index source %s: %w", root, err)
		}
	}
	sort.Strings(records)
	hash := sha256.New()
	for _, record := range records {
		_, _ = hash.Write([]byte(record))
		_, _ = hash.Write([]byte{0})
	}
	// Fingerprint the logical private-ref namespace, not Git's loose/packed
	// storage representation. Routine pack-refs maintenance must not make an
	// otherwise current query index appear stale.
	cmd := exec.Command("git", "--git-dir="+filepath.Join(metadataDir, "git"), "for-each-ref", "--format=%(refname) %(objectname)", "refs/agent-vcs")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("fingerprint checkpoint refs: %w: %s", err, strings.TrimSpace(string(output)))
	}
	_, _ = hash.Write([]byte("private-refs\x00"))
	_, _ = hash.Write(output)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fingerprintRecord(metadataDir, path string, info fs.FileInfo) string {
	relative, err := filepath.Rel(metadataDir, path)
	if err != nil {
		relative = path
	}
	return filepath.ToSlash(relative) + ":" + strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + info.Mode().String()
}
