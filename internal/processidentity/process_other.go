//go:build !linux && !windows && !darwin

package processidentity

func startedAt(pid int) (string, error) {
	return "", ErrUnsupported
}
