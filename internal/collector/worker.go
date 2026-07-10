package collector

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultClaimLimit     = 20
	DefaultWorkerInterval = 5 * time.Second
	MaxRetryDelay         = 6 * time.Hour
)

type Worker struct {
	Store      *Store
	Delivery   DeliveryClient
	Now        func() time.Time
	ClaimLimit int
	Lease      time.Duration
}

type WorkerResult struct {
	Claimed     int
	Delivered   int
	Retryable   int
	Quarantined int
}

func (worker Worker) ProcessOnce(ctx context.Context) (WorkerResult, error) {
	if worker.Store == nil || worker.Delivery == nil {
		return WorkerResult{}, errors.New("collector worker dependencies are required")
	}
	now := worker.now()
	limit := worker.ClaimLimit
	if limit <= 0 {
		limit = DefaultClaimLimit
	}
	items, err := worker.Store.Claim(ctx, limit, now, worker.Lease)
	if err != nil {
		return WorkerResult{}, err
	}
	result := WorkerResult{Claimed: len(items)}
	for _, item := range items {
		delivery, deliveryErr := worker.Delivery.Deliver(ctx, item.Aggregate)
		if deliveryErr != nil && delivery.Disposition == "" {
			delivery = DeliveryResult{Disposition: DeliveryRetryable, Code: "network_error"}
		}
		switch delivery.Disposition {
		case DeliveryDelivered:
			if err := worker.Store.MarkDelivered(ctx, item.BatchID, now); err != nil {
				return result, err
			}
			result.Delivered++
		case DeliveryRejected:
			if err := worker.Store.MarkQuarantined(ctx, item.BatchID, now, stableDeliveryCode(delivery.Code)); err != nil {
				return result, err
			}
			result.Quarantined++
		default:
			delay := retryDelay(item.Attempts)
			if delivery.RetryAfter > delay {
				delay = delivery.RetryAfter
			}
			if err := worker.Store.MarkRetryable(ctx, item.BatchID, now, now.Add(delay), stableDeliveryCode(delivery.Code)); err != nil {
				return result, err
			}
			result.Retryable++
		}
		if deliveryErr != nil && errors.Is(deliveryErr, context.Canceled) {
			return result, deliveryErr
		}
	}
	return result, nil
}

func (worker Worker) Run(ctx context.Context, interval time.Duration, report func(WorkerResult)) error {
	if interval <= 0 {
		interval = DefaultWorkerInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		result, err := worker.ProcessOnce(ctx)
		if err != nil && errors.Is(err, context.Canceled) {
			return err
		}
		if err == nil && report != nil {
			report(result)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (worker Worker) now() time.Time {
	if worker.Now != nil {
		return worker.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Minute
	for index := 1; index < attempt && delay < MaxRetryDelay; index++ {
		delay *= 2
	}
	if delay > MaxRetryDelay {
		return MaxRetryDelay
	}
	return delay
}

func stableDeliveryCode(code string) string {
	switch code {
	case "accepted", "encoding_error", "invalid_ack", "network_error", "posthog_rejected", "posthog_retryable", "request_invalid", "request_too_large", "response_read_error", "response_too_large", "translation_invalid":
		return code
	default:
		return "delivery_error"
	}
}
