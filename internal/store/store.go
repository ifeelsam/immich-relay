// Package store provides the restart-safe SQLite queue used by the relay.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/immich-relay/immich-relay/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.RecoverInterrupted(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS assets (
 id TEXT PRIMARY KEY,
 original_name TEXT NOT NULL,
 mime_type TEXT NOT NULL,
 checksum TEXT NOT NULL DEFAULT '',
 byte_size INTEGER NOT NULL DEFAULT 0,
 asset_created_at TEXT NOT NULL,
 asset_updated_at TEXT NOT NULL,
 state TEXT NOT NULL,
 destination_path TEXT NOT NULL DEFAULT '',
 local_path TEXT NOT NULL DEFAULT '',
 transfer_sha256 TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 next_retry_at TEXT,
 last_error TEXT NOT NULL DEFAULT '',
 batch_id TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS assets_state_retry_idx ON assets(state, next_retry_at);
CREATE INDEX IF NOT EXISTS assets_batch_idx ON assets(batch_id);
CREATE TABLE IF NOT EXISTS batches (
 id TEXT PRIMARY KEY,
 created_at TEXT NOT NULL,
 confirmed_at TEXT,
 cleaned_at TEXT,
 notes TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settings (
 key TEXT PRIMARY KEY,
 value TEXT NOT NULL
);`)
	return err
}

func (s *Store) RecoverInterrupted(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE assets SET state = ?, last_error = CASE WHEN last_error = '' THEN 'interrupted before transfer completed' ELSE last_error END, updated_at = ? WHERE state = ?`, model.StateQueued, now(), model.StateTransferring)
	return err
}

func (s *Store) UpsertDiscovered(ctx context.Context, a model.Asset, initialIgnore bool) (bool, error) {
	if a.ID == "" || a.OriginalName == "" {
		return false, errors.New("asset id and original name are required")
	}
	state := model.StateQueued
	if initialIgnore {
		state = model.StateIgnored
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO assets (id, original_name, mime_type, checksum, byte_size, asset_created_at, asset_updated_at, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`,
		a.ID, a.OriginalName, a.MIMEType, a.Checksum, a.ByteSize, timestamp(a.CreatedAt), timestamp(a.UpdatedAt), state, now(), now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 1 {
		return n == 1, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE assets SET original_name=?, mime_type=?, checksum=?, byte_size=?, asset_updated_at=?, updated_at=? WHERE id=?`,
		a.OriginalName, a.MIMEType, a.Checksum, a.ByteSize, timestamp(a.UpdatedAt), now(), a.ID)
	return false, err
}

func (s *Store) NextTransfer(ctx context.Context, at time.Time) (*model.Asset, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE (state = ? OR (state = ? AND (next_retry_at IS NULL OR next_retry_at <= ?))) ORDER BY asset_created_at, id LIMIT 1`, model.StateQueued, model.StateFailed, timestamp(at))
	a, err := scanAsset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE assets SET state=?, attempts=attempts+1, last_error='', updated_at=? WHERE id=? AND state IN (?, ?)`, model.StateTransferring, now(), a.ID, model.StateQueued, model.StateFailed)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, fmt.Errorf("lease asset %s: queue changed", a.ID)
	}
	a.State = model.StateTransferring
	a.Attempts++
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) MarkDelivered(ctx context.Context, id, destination, localPath, sha, batchID string) error {
	return s.transition(ctx, id, []model.State{model.StateTransferring}, model.StateAwaitingBackupConfirmation,
		`destination_path=?, local_path=?, transfer_sha256=?, batch_id=?, next_retry_at=NULL, last_error=''`, destination, localPath, sha, batchID)
}

func (s *Store) MarkFailed(ctx context.Context, id string, attempt, maxAttempts int, cause error) error {
	state := model.StateFailed
	delay := retryDelay(attempt)
	next := time.Now().UTC().Add(delay)
	message := sanitizeError(cause)
	if attempt >= maxAttempts {
		message = "retry limit reached: " + message
		next = time.Time{}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE assets SET state=?, last_error=?, next_retry_at=?, updated_at=? WHERE id=? AND state=?`, state, message, nullTime(next), now(), id, model.StateTransferring)
	return err
}

// Requeue releases a leased transfer when a pass must pause (for example, the
// phone is low on storage). It deliberately retains the prior error context.
func (s *Store) Requeue(ctx context.Context, id string, cause string) error {
	return s.transition(ctx, id, []model.State{model.StateTransferring}, model.StateQueued, `last_error=?`, cause)
}

// MarkUnsupported records an asset that the initial relay cannot safely place
// on Android. It remains visible to the user and is never silently dropped.
func (s *Store) MarkUnsupported(ctx context.Context, id, reason string) error {
	return s.transition(ctx, id, []model.State{model.StateQueued}, model.StateFailed, `last_error=?, next_retry_at=NULL`, reason)
}

func (s *Store) RetryFailed(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE assets SET state=?, next_retry_at=NULL, last_error='', updated_at=? WHERE state=?`, model.StateQueued, now(), model.StateFailed)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) CreateBatch(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO batches(id, created_at) VALUES (?, ?)`, id, now())
	return err
}

func (s *Store) ConfirmBatch(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE batches SET confirmed_at=? WHERE id=? AND confirmed_at IS NULL`, now(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("batch %q does not exist or is already confirmed", id)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE assets SET state=?, updated_at=? WHERE batch_id=? AND state=?`, model.StateBackupConfirmed, now(), id, model.StateAwaitingBackupConfirmation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConfirmedPaths(ctx context.Context, id string) ([]model.Asset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE batch_id=? AND state=? ORDER BY id`, id, model.StateBackupConfirmed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []model.Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, a)
	}
	return assets, rows.Err()
}

func (s *Store) MarkLocalRemoved(ctx context.Context, id string) error {
	return s.transition(ctx, id, []model.State{model.StateBackupConfirmed}, model.StateLocalRemoved, `local_path=''`)
}

func (s *Store) MarkBatchCleaned(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE batches SET cleaned_at=? WHERE id=? AND confirmed_at IS NOT NULL`, now(), id)
	return err
}

func (s *Store) ListBatches(ctx context.Context) ([]model.Batch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, confirmed_at, cleaned_at, notes FROM batches ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []model.Batch
	for rows.Next() {
		var b model.Batch
		var created string
		var confirmed, cleaned sql.NullString
		if err := rows.Scan(&b.ID, &created, &confirmed, &cleaned, &b.Notes); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if confirmed.Valid {
			if t, err := time.Parse(time.RFC3339Nano, confirmed.String); err == nil {
				b.ConfirmedAt = &t
			}
		}
		if cleaned.Valid {
			if t, err := time.Parse(time.RFC3339Nano, cleaned.String); err == nil {
				b.CleanedAt = &t
			}
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

func (s *Store) List(ctx context.Context, state string) ([]model.Asset, error) {
	query, args := `SELECT `+assetColumns+` FROM assets`, []any{}
	if state != "" {
		query += ` WHERE state=?`
		args = append(args, state)
	}
	query += ` ORDER BY asset_created_at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []model.Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, a)
	}
	return assets, rows.Err()
}

func (s *Store) Counts(ctx context.Context) (map[model.State]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, count(*) FROM assets GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[model.State]int{}
	for rows.Next() {
		var state model.State
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		counts[state] = n
	}
	return counts, rows.Err()
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) transition(ctx context.Context, id string, from []model.State, to model.State, set string, args ...any) error {
	marks := strings.TrimRight(strings.Repeat("?,", len(from)), ",")
	query := `UPDATE assets SET state=?, ` + set + `, updated_at=? WHERE id=? AND state IN (` + marks + `)`
	values := []any{to}
	values = append(values, args...)
	values = append(values, now(), id)
	for _, state := range from {
		values = append(values, state)
	}
	res, err := s.db.ExecContext(ctx, query, values...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("invalid state transition for asset %q", id)
	}
	return nil
}

const assetColumns = `id, original_name, mime_type, checksum, byte_size, asset_created_at, asset_updated_at, state, destination_path, local_path, transfer_sha256, attempts, next_retry_at, last_error, batch_id`

type scanner interface{ Scan(...any) error }

func scanAsset(row scanner) (model.Asset, error) {
	var a model.Asset
	var created, updated string
	var retry sql.NullString
	err := row.Scan(&a.ID, &a.OriginalName, &a.MIMEType, &a.Checksum, &a.ByteSize, &created, &updated, &a.State, &a.DestinationPath, &a.LocalPath, &a.TransferSHA256, &a.Attempts, &retry, &a.LastError, &a.BatchID)
	if err != nil {
		return a, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if retry.Valid {
		v, e := time.Parse(time.RFC3339Nano, retry.String)
		if e == nil {
			a.NextRetryAt = &v
		}
	}
	return a, nil
}
func timestamp(t time.Time) string {
	if t.IsZero() {
		return now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return timestamp(t)
}
func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Second * time.Duration(1<<(attempt-1))
}
func sanitizeError(err error) string {
	return strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
}
