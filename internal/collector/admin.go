package collector

import (
	"context"
	"errors"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

type DeletionProvider interface {
	Request(context.Context, telemetry.UUID) (BulkDeletionResponse, error)
	CountEvents(context.Context, telemetry.UUID) (uint64, error)
}

type DeletionWorkflow struct {
	Store   *Store
	PostHog DeletionProvider
	Now     func() time.Time
}

type DeletionStart struct {
	Collector        DeletionResult
	EventsQueued     bool
	RecordingsQueued bool
	PersonsFound     int
}

func (workflow DeletionWorkflow) Begin(ctx context.Context, id telemetry.UUID) (DeletionStart, error) {
	if workflow.Store == nil || workflow.PostHog == nil {
		return DeletionStart{}, errors.New("deletion workflow dependencies are required")
	}
	collectorResult, err := workflow.Store.BeginDeletion(ctx, id, workflow.now())
	if err != nil {
		return DeletionStart{}, err
	}
	posthog, err := workflow.PostHog.Request(ctx, id)
	if err != nil {
		return DeletionStart{}, err
	}
	return DeletionStart{
		Collector:        collectorResult,
		EventsQueued:     posthog.EventsQueuedForDeletion,
		RecordingsQueued: posthog.RecordingsQueuedForDeletion,
		PersonsFound:     posthog.PersonsFound,
	}, nil
}

func (workflow DeletionWorkflow) Verify(ctx context.Context, id telemetry.UUID) (uint64, error) {
	if workflow.PostHog == nil {
		return 0, errors.New("PostHog deletion provider is required")
	}
	return workflow.PostHog.CountEvents(ctx, id)
}

func (workflow DeletionWorkflow) Complete(ctx context.Context, id telemetry.UUID, derivedCopiesVerified bool) error {
	if workflow.Store == nil || workflow.PostHog == nil {
		return errors.New("deletion workflow dependencies are required")
	}
	if !derivedCopiesVerified {
		return errors.New("exports, caches, and derived copies must be verified before completion")
	}
	remaining, err := workflow.PostHog.CountEvents(ctx, id)
	if err != nil {
		return err
	}
	if remaining != 0 {
		return errors.New("PostHog events remain for the installation ID")
	}
	return workflow.Store.CompleteDeletion(ctx, id, workflow.now())
}

func (workflow DeletionWorkflow) now() time.Time {
	if workflow.Now != nil {
		return workflow.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}
