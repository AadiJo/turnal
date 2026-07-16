package externaladapters

import (
	"fmt"
	"io"

	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

func Run(name string, in io.Reader, out io.Writer) error {
	manifest, ok := Manifest(name)
	if !ok {
		return fmt.Errorf("unknown bundled adapter %q", name)
	}
	normalizer, _ := Normalizer(name)
	return adaptersdk.Serve(in, out, manifest, normalizer)
}
