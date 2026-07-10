package collector

import (
	"context"
	"errors"
	"time"
)

const DefaultCanarySettleDelay = 2 * time.Minute

type CanarySender interface {
	DeliverCanary(context.Context, time.Time) (string, DeliveryResult, error)
}

type CanaryQuery interface {
	CountCanary(context.Context, string) (uint64, error)
}

type CanaryReconciler struct {
	Sender      CanarySender
	Query       CanaryQuery
	Now         func() time.Time
	Wait        func(context.Context, time.Duration) error
	SettleDelay time.Duration
}

type CanaryResult struct {
	SentAt     time.Time
	VerifiedAt time.Time
	Visible    uint64
	Verified   bool
}

func (reconciler CanaryReconciler) RunOnce(ctx context.Context) (CanaryResult, error) {
	if reconciler.Sender == nil || reconciler.Query == nil {
		return CanaryResult{}, errors.New("canary reconciler dependencies are required")
	}
	sentAt := reconciler.now()
	insertID, delivery, err := reconciler.Sender.DeliverCanary(ctx, sentAt)
	if err != nil {
		return CanaryResult{SentAt: sentAt}, err
	}
	if delivery.Disposition != DeliveryDelivered {
		return CanaryResult{SentAt: sentAt}, errors.New("collector canary was not accepted by PostHog")
	}
	delay := reconciler.SettleDelay
	if delay <= 0 {
		delay = DefaultCanarySettleDelay
	}
	wait := reconciler.Wait
	if wait == nil {
		wait = func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if err := wait(ctx, delay); err != nil {
		return CanaryResult{SentAt: sentAt}, err
	}
	visible, err := reconciler.Query.CountCanary(ctx, insertID)
	result := CanaryResult{SentAt: sentAt, VerifiedAt: reconciler.now(), Visible: visible, Verified: err == nil && visible == 1}
	if err != nil {
		return result, err
	}
	if visible != 1 {
		return result, errors.New("collector canary did not reconcile to exactly one event")
	}
	return result, nil
}

func (reconciler CanaryReconciler) now() time.Time {
	if reconciler.Now != nil {
		return reconciler.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}
