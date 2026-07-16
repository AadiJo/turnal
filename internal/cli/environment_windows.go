//go:build windows

package cli

import "strings"

func environmentNamesEqual(left, right string) bool { return strings.EqualFold(left, right) }
