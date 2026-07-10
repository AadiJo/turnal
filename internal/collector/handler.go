package collector

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

const (
	DefaultRateLimitPerMinute = 120
	maxCollectorResponseBytes = 1024
)

type HandlerConfig struct {
	Enabled            bool
	RequireHTTPS       bool
	TrustProxyProto    bool
	RateLimitPerMinute int
	DailyVolumeLimit   uint64
	Now                func() time.Time
	Monitor            *Monitor
}

type Handler struct {
	store   *Store
	config  HandlerConfig
	limiter *ephemeralLimiter
	monitor *Monitor
}

type errorResponse struct {
	Code string `json:"code"`
}

type acceptanceResponse struct {
	Status      string `json:"status"`
	Accepted    int    `json:"accepted"`
	Duplicates  int    `json:"duplicates"`
	Quarantined int    `json:"quarantined"`
}

func NewHandler(store *Store, config HandlerConfig) (*Handler, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("collector store is required")
	}
	limit := config.RateLimitPerMinute
	if limit <= 0 {
		limit = DefaultRateLimitPerMinute
	}
	monitor := config.Monitor
	if monitor == nil {
		monitor = NewMonitor()
	}
	return &Handler{
		store:   store,
		config:  config,
		limiter: newEphemeralLimiter(limit, time.Minute),
		monitor: monitor,
	}, nil
}

func (handler *Handler) Monitor() *Monitor { return handler.monitor }

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/healthz":
		handler.health(writer, request)
	case "/v1/batch":
		handler.batch(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not_found")
	}
}

func (handler *Handler) health(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if _, err := handler.store.Stats(request.Context()); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) batch(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	handler.monitor.RecordRequest()
	if !handler.config.Enabled {
		handler.monitor.RecordKillSwitch()
		writer.Header().Set("Retry-After", fmt.Sprintf("%.0f", telemetry.KillSwitchBackoff.Seconds()))
		writeError(writer, http.StatusGone, "collection_disabled")
		return
	}
	if handler.config.RequireHTTPS && !requestIsHTTPS(request, handler.config.TrustProxyProto) {
		handler.monitor.RecordRejected()
		writeError(writer, http.StatusUpgradeRequired, "https_required")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		handler.monitor.RecordRejected()
		writeError(writer, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	now := handler.now()
	if !handler.limiter.Allow(ephemeralClientKey(request), now) {
		handler.monitor.RecordRateLimited()
		writer.Header().Set("Retry-After", "60")
		writeError(writer, http.StatusTooManyRequests, "rate_limited")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, telemetry.MaxRequestBytes)
	payload, err := decodeCollectorRequest(request.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			handler.monitor.RecordSchemaRejected(nil)
			writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large")
			return
		}
		handler.monitor.RecordSchemaRejected(nil)
		writeError(writer, http.StatusBadRequest, "invalid_payload")
		return
	}
	if err := validateCollectorRequest(payload, now); err != nil {
		handler.monitor.RecordSchemaRejected(requestVersions(payload))
		writeError(writer, http.StatusBadRequest, "invalid_payload")
		return
	}
	result, err := handler.store.Accept(request.Context(), payload.Batches, AcceptOptions{
		Now:              now,
		DailyVolumeLimit: handler.config.DailyVolumeLimit,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrBatchConflict):
			handler.monitor.RecordConflict()
			writeError(writer, http.StatusConflict, "batch_conflict")
		case errors.Is(err, ErrInstallationDenied):
			writeError(writer, http.StatusGone, "installation_deleted")
		default:
			handler.monitor.RecordServerError()
			writeError(writer, http.StatusServiceUnavailable, "storage_unavailable")
		}
		return
	}
	handler.monitor.RecordAcceptance(result)
	writeJSON(writer, http.StatusAccepted, acceptanceResponse{
		Status:      "durably_accepted",
		Accepted:    result.Accepted,
		Duplicates:  result.Duplicates,
		Quarantined: result.Quarantined,
	})
}

func requestVersions(payload telemetry.CollectorRequest) []string {
	versions := make([]string, 0, len(payload.Batches))
	for _, batch := range payload.Batches {
		versions = append(versions, batch.Build.Version)
	}
	return versions
}

func decodeCollectorRequest(reader io.Reader) (telemetry.CollectorRequest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var payload telemetry.CollectorRequest
	if err := decoder.Decode(&payload); err != nil {
		return telemetry.CollectorRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return telemetry.CollectorRequest{}, errors.New("trailing JSON value")
		}
		return telemetry.CollectorRequest{}, err
	}
	return payload, nil
}

func validateCollectorRequest(payload telemetry.CollectorRequest, now time.Time) error {
	if payload.SchemaVersion != telemetry.SchemaVersion {
		return errors.New("unsupported collector schema")
	}
	if len(payload.Batches) == 0 || len(payload.Batches) > telemetry.MaxBatchesPerRequest {
		return errors.New("invalid collector batch count")
	}
	today := midnight(now)
	oldest := today.AddDate(0, 0, -14)
	seen := make(map[string]struct{}, len(payload.Batches))
	for _, batch := range payload.Batches {
		if err := batch.Validate(); err != nil {
			return err
		}
		date, _ := time.Parse(time.DateOnly, batch.Date)
		if date.Before(oldest) || date.After(today) {
			return errors.New("aggregate date outside acceptance window")
		}
		if _, duplicate := seen[batch.BatchID.String()]; duplicate {
			return errors.New("duplicate batch ID in request")
		}
		seen[batch.BatchID.String()] = struct{}{}
	}
	return nil
}

func requestIsHTTPS(request *http.Request, trustProxy bool) bool {
	if request.TLS != nil || request.URL.Scheme == "https" {
		return true
	}
	return trustProxy && strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https")
}

func ephemeralClientKey(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func (handler *Handler) now() time.Time {
	if handler.config.Now != nil {
		return handler.config.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func midnight(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, errorResponse{Code: code})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxCollectorResponseBytes {
		status = http.StatusInternalServerError
		data = []byte(`{"code":"internal_error"}`)
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(append(data, '\n'))
}

type limiterWindow struct {
	started time.Time
	count   int
}

type ephemeralLimiter struct {
	mu       sync.Mutex
	limit    int
	duration time.Duration
	windows  map[string]limiterWindow
}

func newEphemeralLimiter(limit int, duration time.Duration) *ephemeralLimiter {
	return &ephemeralLimiter{limit: limit, duration: duration, windows: make(map[string]limiterWindow)}
}

func (limiter *ephemeralLimiter) Allow(key string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	window := limiter.windows[key]
	if window.started.IsZero() || !now.Before(window.started.Add(limiter.duration)) {
		window = limiterWindow{started: now}
	}
	window.count++
	limiter.windows[key] = window
	if len(limiter.windows) > 4096 {
		for existing, candidate := range limiter.windows {
			if !now.Before(candidate.started.Add(limiter.duration)) {
				delete(limiter.windows, existing)
			}
		}
	}
	return window.count <= limiter.limit
}
