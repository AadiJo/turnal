package viewer

import (
	"embed"
	"io/fs"
)

// webAssets contains the deterministic production build. Keeping the build
// output in the repository lets direct Go installs embed the viewer without a
// Node toolchain.
//
//go:embed web/dist
var webAssets embed.FS

func productionAssets() (fs.FS, error) {
	return fs.Sub(webAssets, "web/dist")
}
