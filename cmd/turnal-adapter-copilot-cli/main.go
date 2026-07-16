package main

import (
	"fmt"
	"os"

	"github.com/AadiJo/turnal/internal/externaladapters"
)

func main() {
	if err := externaladapters.Run("copilot-cli", os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
