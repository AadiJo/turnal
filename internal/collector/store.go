package collector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
	_ "modernc.org/sqlite"
)

const (
	StateDurablyAccepted = "durably_accepted"
	StateForwarding      = "forwarding"
	StateRetryable       = "retryable"
	StateDelivered       = "delivered"
	StateQuarantined     = "quarantined"

	DefaultDailyVolumeLimit = 100_000
	DefaultForwardLease     = 5 * time.Minute
)

var (
	ErrBatchConflict      = errors.New("batch ID conflicts with accepted payload")
	ErrInstallationDenied = errors.New("installation ID is denied")
)

type AcceptHooks struct {
	AfterInsert  func(telemetry.UUID) error
	BeforeCommit func() error
	AfterCommit  func() error
}

type Store struct {
	db          *sql.DB
	AcceptHooks AcceptHooks
}

type AcceptOptions struct {
	Now              time.Time
	DailyVolumeLimit uint64
}

type AcceptResult struct {
	Accepted    int `json:"accepted"`
	Duplicates  int `json:"duplicates"`
	Quarantined int `json:"quarantined"`
}

type OutboxItem struct {
	BatchID    telemetry.UUID
	Aggregate  telemetry.DailyAggregate
	State      string
	Attempts   int
	AcceptedAt time.Time
	UpdatedAt  time.Time
}

type OutboxStats struct {
	DurablyAccepted int
	Forwarding      int
	Retryable       int
	Delivered       int
	Quarantined     int
	OldestPending   time.Time
}

type DeletionResult struct {
	OutboxRows      int
	DailyVolumeRows int
}

type PurgeOptions struct {
	Now                        time.Time
	DeliveredMetadataRetention time.Duration
	QuarantineRetention        time.Duration
	DailyVolumeRetention       time.Duration
	DeletionAuditRetention     time.Duration
}

type PurgeResult struct {
	OutboxRows   int
	VolumeRows   int
	DeletionRows int
}

func OpenStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("collector database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create collector database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open collector database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *Store) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = FULL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS outbox (
			batch_id TEXT PRIMARY KEY,
			anonymous_id TEXT NOT NULL,
			event_date TEXT NOT NULL,
			payload BLOB,
			payload_sha256 BLOB NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('durably_accepted','forwarding','retryable','delivered','quarantined')),
			quarantine_code TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
			next_attempt_at TEXT NOT NULL,
			accepted_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			delivered_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS outbox_delivery_idx ON outbox(state, next_attempt_at, accepted_at)`,
		`CREATE INDEX IF NOT EXISTS outbox_installation_idx ON outbox(anonymous_id)`,
		`CREATE TABLE IF NOT EXISTS daily_volume (
			anonymous_id TEXT NOT NULL,
			event_date TEXT NOT NULL,
			metric_count INTEGER NOT NULL CHECK (metric_count >= 0),
			updated_at TEXT NOT NULL,
			PRIMARY KEY (anonymous_id, event_date)
		)`,
		`CREATE TABLE IF NOT EXISTS deletion_denylist (
			anonymous_id TEXT PRIMARY KEY,
			state TEXT NOT NULL CHECK (state IN ('pending','completed')),
			requested_at TEXT NOT NULL,
			completed_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS deletion_audit (
			anonymous_id TEXT PRIMARY KEY,
			outbox_rows INTEGER NOT NULL,
			daily_volume_rows INTEGER NOT NULL,
			requested_at TEXT NOT NULL,
			verified_at TEXT
		)`,
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize collector database: %w", err)
		}
	}
	return nil
}

func (store *Store) Accept(ctx context.Context, batches []telemetry.DailyAggregate, options AcceptOptions) (AcceptResult, error) {
	if len(batches) == 0 {
		return AcceptResult{}, errors.New("at least one batch is required")
	}
	now := options.Now.UTC().Truncate(time.Second)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Second)
	}
	volumeLimit := options.DailyVolumeLimit
	if volumeLimit == 0 {
		volumeLimit = DefaultDailyVolumeLimit
	}
	stamp := now.Format(time.RFC3339)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return AcceptResult{}, fmt.Errorf("begin outbox acceptance: %w", err)
	}
	defer tx.Rollback()

	result := AcceptResult{}
	seen := make(map[string]struct{}, len(batches))
	for _, batch := range batches {
		if err := batch.Validate(); err != nil {
			return AcceptResult{}, err
		}
		batchID := batch.BatchID.String()
		if _, duplicate := seen[batchID]; duplicate {
			return AcceptResult{}, fmt.Errorf("duplicate batch ID %s in request", batchID)
		}
		seen[batchID] = struct{}{}
		denied, err := installationDenied(ctx, tx, batch.AnonymousID.String())
		if err != nil {
			return AcceptResult{}, err
		}
		if denied {
			return AcceptResult{}, ErrInstallationDenied
		}
		canonical, err := telemetry.EncodeDailyAggregate(batch)
		if err != nil {
			return AcceptResult{}, err
		}
		digest := sha256.Sum256(canonical)
		var existingDigest []byte
		var existingState string
		err = tx.QueryRowContext(ctx, `SELECT payload_sha256, state FROM outbox WHERE batch_id = ?`, batchID).Scan(&existingDigest, &existingState)
		if err == nil {
			if !bytes.Equal(existingDigest, digest[:]) {
				return AcceptResult{}, ErrBatchConflict
			}
			result.Duplicates++
			if existingState == StateQuarantined {
				result.Quarantined++
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return AcceptResult{}, fmt.Errorf("inspect outbox batch: %w", err)
		}

		volume, err := aggregateVolume(batch)
		if err != nil {
			return AcceptResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO daily_volume (anonymous_id, event_date, metric_count, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(anonymous_id, event_date) DO UPDATE SET
				metric_count = daily_volume.metric_count + excluded.metric_count,
				updated_at = excluded.updated_at`,
			batch.AnonymousID.String(), batch.Date, volume, stamp,
		); err != nil {
			return AcceptResult{}, fmt.Errorf("update collector volume budget: %w", err)
		}
		var dailyVolume uint64
		if err := tx.QueryRowContext(ctx, `SELECT metric_count FROM daily_volume WHERE anonymous_id = ? AND event_date = ?`, batch.AnonymousID.String(), batch.Date).Scan(&dailyVolume); err != nil {
			return AcceptResult{}, fmt.Errorf("read collector volume budget: %w", err)
		}
		state := StateDurablyAccepted
		quarantineCode := ""
		if dailyVolume > volumeLimit {
			state = StateQuarantined
			quarantineCode = "volume_anomaly"
			result.Quarantined++
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO outbox (
				batch_id, anonymous_id, event_date, payload, payload_sha256, state,
				quarantine_code, attempts, next_attempt_at, accepted_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			batchID, batch.AnonymousID.String(), batch.Date, canonical, digest[:], state,
			quarantineCode, stamp, stamp, stamp,
		); err != nil {
			return AcceptResult{}, fmt.Errorf("insert durable outbox batch: %w", err)
		}
		result.Accepted++
		if store.AcceptHooks.AfterInsert != nil {
			if err := store.AcceptHooks.AfterInsert(batch.BatchID); err != nil {
				return AcceptResult{}, err
			}
		}
	}
	if store.AcceptHooks.BeforeCommit != nil {
		if err := store.AcceptHooks.BeforeCommit(); err != nil {
			return AcceptResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AcceptResult{}, fmt.Errorf("commit durable outbox acceptance: %w", err)
	}
	if store.AcceptHooks.AfterCommit != nil {
		if err := store.AcceptHooks.AfterCommit(); err != nil {
			return AcceptResult{}, err
		}
	}
	return result, nil
}

func installationDenied(ctx context.Context, tx *sql.Tx, anonymousID string) (bool, error) {
	var value int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM deletion_denylist WHERE anonymous_id = ?`, anonymousID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect deletion denylist: %w", err)
	}
	return true, nil
}

func aggregateVolume(batch telemetry.DailyAggregate) (uint64, error) {
	var total uint64 = 1
	for _, metric := range batch.Metrics {
		if metric.Count > ^uint64(0)-total {
			return 0, errors.New("aggregate volume overflow")
		}
		total += metric.Count
	}
	return total, nil
}

func (store *Store) Claim(ctx context.Context, limit int, now time.Time, lease time.Duration) ([]OutboxItem, error) {
	if limit <= 0 {
		return nil, nil
	}
	if lease <= 0 {
		lease = DefaultForwardLease
	}
	now = now.UTC().Truncate(time.Second)
	stamp := now.Format(time.RFC3339)
	staleBefore := now.Add(-lease).Format(time.RFC3339)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE outbox SET state = ?, updated_at = ?
		WHERE state = ? AND updated_at < ?`,
		StateRetryable, stamp, StateForwarding, staleBefore,
	); err != nil {
		return nil, fmt.Errorf("requeue stale outbox claims: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT batch_id, payload, state, attempts, accepted_at, updated_at
		FROM outbox
		WHERE state IN (?, ?) AND next_attempt_at <= ? AND payload IS NOT NULL
		ORDER BY accepted_at, batch_id
		LIMIT ?`, StateDurablyAccepted, StateRetryable, stamp, limit)
	if err != nil {
		return nil, fmt.Errorf("select outbox claims: %w", err)
	}
	defer rows.Close()
	var items []OutboxItem
	for rows.Next() {
		var batchID string
		var payload []byte
		var item OutboxItem
		var acceptedAt, updatedAt string
		if err := rows.Scan(&batchID, &payload, &item.State, &item.Attempts, &acceptedAt, &updatedAt); err != nil {
			return nil, err
		}
		parsedID, err := telemetry.ParseUUID(batchID)
		if err != nil {
			return nil, err
		}
		aggregate, err := telemetry.DecodeDailyAggregate(payload)
		if err != nil {
			return nil, err
		}
		item.BatchID = parsedID
		item.Aggregate = aggregate
		item.AcceptedAt, _ = time.Parse(time.RFC3339, acceptedAt)
		item.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		result, err := tx.ExecContext(ctx, `
			UPDATE outbox SET state = ?, attempts = attempts + 1, updated_at = ?
			WHERE batch_id = ? AND state IN (?, ?)`,
			StateForwarding, stamp, items[index].BatchID.String(), StateDurablyAccepted, StateRetryable,
		)
		if err != nil {
			return nil, err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return nil, errors.New("outbox claim changed concurrently")
		}
		items[index].State = StateForwarding
		items[index].Attempts++
		items[index].UpdatedAt = now
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (store *Store) MarkDelivered(ctx context.Context, batchID telemetry.UUID, now time.Time) error {
	stamp := now.UTC().Truncate(time.Second).Format(time.RFC3339)
	result, err := store.db.ExecContext(ctx, `
		UPDATE outbox
		SET state = ?, payload = NULL, delivered_at = ?, updated_at = ?, quarantine_code = ''
		WHERE batch_id = ? AND state = ?`, StateDelivered, stamp, stamp, batchID.String(), StateForwarding)
	return expectOneRow(result, err, "mark outbox batch delivered")
}

func (store *Store) MarkRetryable(ctx context.Context, batchID telemetry.UUID, now, next time.Time, code string) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE outbox
		SET state = ?, next_attempt_at = ?, updated_at = ?, quarantine_code = ?
		WHERE batch_id = ? AND state = ?`,
		StateRetryable, next.UTC().Truncate(time.Second).Format(time.RFC3339), now.UTC().Truncate(time.Second).Format(time.RFC3339), code, batchID.String(), StateForwarding,
	)
	return expectOneRow(result, err, "mark outbox batch retryable")
}

func (store *Store) MarkQuarantined(ctx context.Context, batchID telemetry.UUID, now time.Time, code string) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE outbox
		SET state = ?, updated_at = ?, quarantine_code = ?
		WHERE batch_id = ? AND state = ?`,
		StateQuarantined, now.UTC().Truncate(time.Second).Format(time.RFC3339), code, batchID.String(), StateForwarding,
	)
	return expectOneRow(result, err, "quarantine outbox batch")
}

func expectOneRow(result sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if changed != 1 {
		return fmt.Errorf("%s: batch is not in the expected state", action)
	}
	return nil
}

func (store *Store) Stats(ctx context.Context) (OutboxStats, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM outbox GROUP BY state`)
	if err != nil {
		return OutboxStats{}, err
	}
	defer rows.Close()
	var stats OutboxStats
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return OutboxStats{}, err
		}
		switch state {
		case StateDurablyAccepted:
			stats.DurablyAccepted = count
		case StateForwarding:
			stats.Forwarding = count
		case StateRetryable:
			stats.Retryable = count
		case StateDelivered:
			stats.Delivered = count
		case StateQuarantined:
			stats.Quarantined = count
		}
	}
	if err := rows.Err(); err != nil {
		return OutboxStats{}, err
	}
	if err := rows.Close(); err != nil {
		return OutboxStats{}, err
	}
	var oldest sql.NullString
	if err := store.db.QueryRowContext(ctx, `
		SELECT MIN(accepted_at) FROM outbox WHERE state IN (?, ?, ?)`,
		StateDurablyAccepted, StateForwarding, StateRetryable,
	).Scan(&oldest); err != nil {
		return OutboxStats{}, err
	}
	if oldest.Valid {
		stats.OldestPending, _ = time.Parse(time.RFC3339, oldest.String)
	}
	return stats, nil
}

func (store *Store) AcceptanceVolume(ctx context.Context, day time.Time) (uint64, error) {
	day = time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC)
	var count uint64
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM outbox WHERE accepted_at >= ? AND accepted_at < ?`,
		day.Format(time.RFC3339), day.AddDate(0, 0, 1).Format(time.RFC3339),
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count daily accepted batches: %w", err)
	}
	return count, nil
}

func (store *Store) BeginDeletion(ctx context.Context, id telemetry.UUID, now time.Time) (DeletionResult, error) {
	if !id.Valid() {
		return DeletionResult{}, errors.New("invalid installation ID")
	}
	stamp := now.UTC().Truncate(time.Second).Format(time.RFC3339)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return DeletionResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deletion_denylist (anonymous_id, state, requested_at)
		VALUES (?, 'pending', ?)
		ON CONFLICT(anonymous_id) DO UPDATE SET state = 'pending', requested_at = excluded.requested_at, completed_at = NULL`, id.String(), stamp); err != nil {
		return DeletionResult{}, err
	}
	outboxResult, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE anonymous_id = ?`, id.String())
	if err != nil {
		return DeletionResult{}, err
	}
	volumeResult, err := tx.ExecContext(ctx, `DELETE FROM daily_volume WHERE anonymous_id = ?`, id.String())
	if err != nil {
		return DeletionResult{}, err
	}
	outboxRows, _ := outboxResult.RowsAffected()
	volumeRows, _ := volumeResult.RowsAffected()
	result := DeletionResult{OutboxRows: int(outboxRows), DailyVolumeRows: int(volumeRows)}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO deletion_audit (anonymous_id, outbox_rows, daily_volume_rows, requested_at, verified_at)
		VALUES (?, ?, ?, ?, NULL)
		ON CONFLICT(anonymous_id) DO UPDATE SET
			outbox_rows = excluded.outbox_rows,
			daily_volume_rows = excluded.daily_volume_rows,
			requested_at = excluded.requested_at,
			verified_at = NULL`, id.String(), result.OutboxRows, result.DailyVolumeRows, stamp); err != nil {
		return DeletionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeletionResult{}, err
	}
	return result, nil
}

func (store *Store) CompleteDeletion(ctx context.Context, id telemetry.UUID, now time.Time) error {
	stamp := now.UTC().Truncate(time.Second).Format(time.RFC3339)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE deletion_denylist SET state = 'completed', completed_at = ? WHERE anonymous_id = ?`, stamp, id.String()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deletion_audit SET verified_at = ? WHERE anonymous_id = ?`, stamp, id.String()); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) InstallationDenied(ctx context.Context, id telemetry.UUID) (bool, error) {
	var value int
	err := store.db.QueryRowContext(ctx, `SELECT 1 FROM deletion_denylist WHERE anonymous_id = ?`, id.String()).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (store *Store) PurgeExpired(ctx context.Context, options PurgeOptions) (PurgeResult, error) {
	now := options.Now.UTC().Truncate(time.Second)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Second)
	}
	if options.DeliveredMetadataRetention <= 0 {
		options.DeliveredMetadataRetention = 7 * 24 * time.Hour
	}
	if options.QuarantineRetention <= 0 {
		options.QuarantineRetention = 7 * 24 * time.Hour
	}
	if options.DailyVolumeRetention <= 0 {
		options.DailyVolumeRetention = 14 * 24 * time.Hour
	}
	if options.DeletionAuditRetention <= 0 {
		options.DeletionAuditRetention = 90 * 24 * time.Hour
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PurgeResult{}, err
	}
	defer tx.Rollback()
	delivered, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE state = ? AND delivered_at < ?`, StateDelivered, now.Add(-options.DeliveredMetadataRetention).Format(time.RFC3339))
	if err != nil {
		return PurgeResult{}, err
	}
	quarantined, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE state = ? AND updated_at < ?`, StateQuarantined, now.Add(-options.QuarantineRetention).Format(time.RFC3339))
	if err != nil {
		return PurgeResult{}, err
	}
	volumeCutoff := now.Add(-options.DailyVolumeRetention).Format(time.DateOnly)
	volume, err := tx.ExecContext(ctx, `DELETE FROM daily_volume WHERE event_date < ?`, volumeCutoff)
	if err != nil {
		return PurgeResult{}, err
	}
	auditCutoff := now.Add(-options.DeletionAuditRetention).Format(time.RFC3339)
	audit, err := tx.ExecContext(ctx, `DELETE FROM deletion_audit WHERE verified_at IS NOT NULL AND verified_at < ?`, auditCutoff)
	if err != nil {
		return PurgeResult{}, err
	}
	denylist, err := tx.ExecContext(ctx, `DELETE FROM deletion_denylist WHERE state = 'completed' AND completed_at < ?`, auditCutoff)
	if err != nil {
		return PurgeResult{}, err
	}
	deliveredRows, _ := delivered.RowsAffected()
	quarantinedRows, _ := quarantined.RowsAffected()
	volumeRows, _ := volume.RowsAffected()
	auditRows, _ := audit.RowsAffected()
	denylistRows, _ := denylist.RowsAffected()
	result := PurgeResult{
		OutboxRows:   int(deliveredRows + quarantinedRows),
		VolumeRows:   int(volumeRows),
		DeletionRows: int(auditRows + denylistRows),
	}
	if err := tx.Commit(); err != nil {
		return PurgeResult{}, err
	}
	return result, nil
}
