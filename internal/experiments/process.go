package experiments

type forkProcessController interface {
	AfterStart() error
	Cancel() error
	Close() error
}
