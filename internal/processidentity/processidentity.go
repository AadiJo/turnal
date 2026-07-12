package processidentity

import (
	"fmt"
	"os"
)

type Identity struct {
	PID     int
	Started string
}

func Current() (Identity, error) {
	pid := os.Getpid()
	started, err := startedAt(pid)
	if err != nil {
		return Identity{}, fmt.Errorf("inspect current process identity: %w", err)
	}
	return Identity{PID: pid, Started: started}, nil
}

func Matches(pid int, started string) (bool, error) {
	if pid <= 0 || started == "" {
		return false, fmt.Errorf("process identity requires a positive pid and creation token")
	}
	current, err := startedAt(pid)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return current == started, nil
}
