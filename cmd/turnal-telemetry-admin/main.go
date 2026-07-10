package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/collector"
	"github.com/AadiJo/turnal/internal/telemetry"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "turnal-telemetry-admin: operation incomplete")
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: turnal-telemetry-admin <delete|verify|complete> <installation-uuid>")
	}
	id, err := telemetry.ParseUUID(args[1])
	if err != nil {
		return err
	}
	projectID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("POSTHOG_PROJECT_ID")))
	if err != nil || projectID <= 0 {
		return fmt.Errorf("POSTHOG_PROJECT_ID is required")
	}
	store, err := collector.OpenStore(envOr("TURNAL_COLLECTOR_DB", "data/collector.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	posthog, err := collector.NewPostHogDeletionClient(collector.PostHogDeletionConfig{
		AppHost:        envOr("POSTHOG_APP_HOST", collector.PostHogUSAppHost),
		ProjectID:      projectID,
		PersonalAPIKey: os.Getenv("POSTHOG_PERSONAL_API_KEY"),
	})
	if err != nil {
		return err
	}
	workflow := collector.DeletionWorkflow{Store: store, PostHog: posthog}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	switch args[0] {
	case "delete":
		result, err := workflow.Begin(ctx, id)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{
			"status":                    "pending",
			"collector_outbox_deleted":  result.Collector.OutboxRows,
			"collector_volume_deleted":  result.Collector.DailyVolumeRows,
			"posthog_events_queued":     result.EventsQueued,
			"posthog_recordings_queued": result.RecordingsQueued,
		})
	case "verify":
		remaining, err := workflow.Verify(ctx, id)
		if err != nil {
			return err
		}
		if err := encoder.Encode(map[string]any{"status": verificationStatus(remaining), "remaining_events": remaining}); err != nil {
			return err
		}
		if remaining != 0 {
			return fmt.Errorf("events remain")
		}
		return nil
	case "complete":
		derivedVerified := envBool("TURNAL_DERIVED_DELETION_VERIFIED", false)
		if err := workflow.Complete(ctx, id, derivedVerified); err != nil {
			return err
		}
		return encoder.Encode(map[string]string{"status": "verified_complete"})
	default:
		return fmt.Errorf("unknown operation")
	}
}

func verificationStatus(remaining uint64) string {
	if remaining == 0 {
		return "posthog_events_absent"
	}
	return "pending"
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}
