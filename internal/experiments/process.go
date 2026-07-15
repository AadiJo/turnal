package experiments

import "errors"

var errForkWaitMainUnsupported = errors.New("fork controller cannot observe main process exit before reap")

type forkProcessController interface {
	AfterStart() error
	WaitMain() error
	Cancel() error
	Close() error
}
