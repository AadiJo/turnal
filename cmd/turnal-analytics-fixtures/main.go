package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/AadiJo/turnal/internal/analytics"
)

func main() {
	aggregates, expected, err := analytics.SyntheticFixture()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate analytics fixture: failed")
		os.Exit(1)
	}
	payload := struct {
		Synthetic  bool                           `json:"synthetic"`
		Aggregates any                            `json:"aggregates"`
		Expected   analytics.SyntheticExpectation `json:"expected"`
	}{
		Synthetic:  true,
		Aggregates: aggregates,
		Expected:   expected,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		fmt.Fprintln(os.Stderr, "encode analytics fixture: failed")
		os.Exit(1)
	}
}
