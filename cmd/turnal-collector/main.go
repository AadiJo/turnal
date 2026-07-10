package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AadiJo/turnal/internal/collector"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "turnal-collector: fatal collector error")
		os.Exit(1)
	}
}

func run() error {
	logger := log.New(os.Stderr, "turnal-collector: ", log.LstdFlags|log.LUTC)
	databasePath := envOr("TURNAL_COLLECTOR_DB", "data/collector.db")
	store, err := collector.OpenStore(databasePath)
	if err != nil {
		return err
	}
	defer store.Close()

	handler, err := collector.NewHandler(store, collector.HandlerConfig{
		Enabled:            envBool("TURNAL_COLLECTOR_ENABLED", false),
		RequireHTTPS:       envBool("TURNAL_COLLECTOR_REQUIRE_HTTPS", true),
		TrustProxyProto:    envBool("TURNAL_COLLECTOR_TRUST_PROXY_PROTO", false),
		RateLimitPerMinute: envInt("TURNAL_COLLECTOR_RATE_LIMIT", collector.DefaultRateLimitPerMinute),
		DailyVolumeLimit:   uint64(envInt("TURNAL_COLLECTOR_DAILY_VOLUME_LIMIT", collector.DefaultDailyVolumeLimit)),
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go runRetention(ctx, store, logger)
	if token := strings.TrimSpace(os.Getenv("POSTHOG_PROJECT_TOKEN")); token != "" {
		posthog, err := collector.NewPostHogClient(collector.PostHogConfig{
			Host:  envOr("POSTHOG_HOST", collector.PostHogUSHost),
			Token: token,
		})
		if err != nil {
			return err
		}
		worker := collector.Worker{Store: store, Delivery: posthog}
		go func() {
			_ = worker.Run(ctx, collector.DefaultWorkerInterval, func(result collector.WorkerResult) {
				if result.Claimed > 0 {
					logger.Printf("delivery cycle claimed=%d delivered=%d retryable=%d quarantined=%d", result.Claimed, result.Delivered, result.Retryable, result.Quarantined)
				}
			})
		}()
	} else {
		logger.Print("PostHog forwarding paused: project token is not configured")
	}

	server := &http.Server{
		Addr:              envOr("TURNAL_COLLECTOR_ADDR", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(shutdownDone)
	}()

	logger.Printf("listening on %s collection_enabled=%t", server.Addr, envBool("TURNAL_COLLECTOR_ENABLED", false))
	certificate := strings.TrimSpace(os.Getenv("TURNAL_COLLECTOR_TLS_CERT"))
	key := strings.TrimSpace(os.Getenv("TURNAL_COLLECTOR_TLS_KEY"))
	if (certificate == "") != (key == "") {
		return errors.New("collector TLS certificate and key must be configured together")
	}
	if certificate != "" {
		err = server.ListenAndServeTLS(certificate, key)
	} else {
		err = server.ListenAndServe()
	}
	stop()
	<-shutdownDone
	return err
}

func runRetention(ctx context.Context, store *collector.Store, logger *log.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		result, err := store.PurgeExpired(ctx, collector.PurgeOptions{Now: time.Now()})
		if err == nil && (result.OutboxRows > 0 || result.VolumeRows > 0 || result.DeletionRows > 0) {
			logger.Printf("retention purge outbox=%d volume=%d deletion=%d", result.OutboxRows, result.VolumeRows, result.DeletionRows)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
