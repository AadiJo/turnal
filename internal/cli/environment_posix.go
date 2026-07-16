//go:build !windows

package cli

func environmentNamesEqual(left, right string) bool { return left == right }
