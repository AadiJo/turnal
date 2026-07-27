package externaladapters

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/AadiJo/turnal/internal/buildinfo"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
)

func RunCommand(name string, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
		if _, ok := Manifest(name); !ok {
			return fmt.Errorf("unknown bundled adapter %q", name)
		}
		return json.NewEncoder(out).Encode(buildinfo.Current())
	}
	return Run(name, in, out)
}

func Run(name string, in io.Reader, out io.Writer) error {
	manifest, ok := Manifest(name)
	if !ok {
		return fmt.Errorf("unknown bundled adapter %q", name)
	}
	normalizer, _ := Normalizer(name)
	return adaptersdk.Serve(in, out, manifest, normalizer)
}
