package main

import (
	"fmt"
	"os"

	"github.com/AadiJo/turnal/internal/externaladapters"
)

func main() {
	if err := externaladapters.RunCommand("copilot-cli", os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
