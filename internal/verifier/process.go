package verifier

type processController interface {
	AfterStart() error
	Cancel() error
	Close() error
}
