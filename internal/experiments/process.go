package experiments

type forkProcessController interface {
	AfterStart() error
	WaitMain() error
	Cancel() error
	Close() error
}
