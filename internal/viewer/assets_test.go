package viewer

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedViewerAssetsStayWithinBudget(t *testing.T) {
	assets, err := productionAssets()
	if err != nil {
		t.Fatal(err)
	}
	var rawBytes int
	var compressed bytes.Buffer
	zipper, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	err = fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if filepath.Ext(path) == ".map" {
			t.Fatalf("release viewer contains source map %s", path)
		}
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		rawBytes += len(data)
		_, err = zipper.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() > 1536*1024 {
		t.Fatalf("compressed viewer assets = %d bytes, limit = %d", compressed.Len(), 1536*1024)
	}
	if rawBytes == 0 {
		t.Fatal("embedded viewer assets are empty")
	}
}

func TestEmbeddedViewerHasNoRemoteRuntimeDependencies(t *testing.T) {
	assets, err := productionAssets()
	if err != nil {
		t.Fatal(err)
	}
	err = fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if filepath.Ext(path) != ".html" && filepath.Ext(path) != ".css" && filepath.Ext(path) != ".js" {
			return nil
		}
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		text := string(data)
		for _, namespace := range []string{
			"http://www.w3.org/2000/svg",
			"http://www.w3.org/1998/Math/MathML",
			"http://www.w3.org/1999/xhtml",
		} {
			text = strings.ReplaceAll(text, namespace, "")
		}
		for _, prefix := range []string{"https://", "http://", "//cdn."} {
			if strings.Contains(text, prefix) {
				t.Fatalf("release viewer asset %s contains remote runtime URL prefix %q", path, prefix)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
