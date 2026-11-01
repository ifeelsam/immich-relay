// Package syncer connects Immich discovery, the durable queue, and a physical
// Android device. It is intentionally conservative: a successful transfer is
// awaiting user confirmation, never a claim of Google Photos backup.
package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/immich-relay/immich-relay/internal/android"
	"github.com/immich-relay/immich-relay/internal/config"
	"github.com/immich-relay/immich-relay/internal/immich"
	"github.com/immich-relay/immich-relay/internal/model"
	"github.com/immich-relay/immich-relay/internal/store"
)

const gib = int64(1024 * 1024 * 1024)

type Engine struct {
	Config   config.Config
	Store    *store.Store
	Immich   immich.Source
	Device   android.Device
	CacheDir string
	Log      *slog.Logger
	Now      func() time.Time
}

type Report struct {
	Discovered     int
	Queued         int
	Transferred    int
	Skipped        int
	Failed         int
	DryRun         bool
	StorageLow     bool
	AvailableBytes int64
	BatchID        string
}

func (e *Engine) RunOnce(ctx context.Context, dryRun bool) (Report, error) {
	if err := e.validate(); err != nil {
		return Report{}, err
	}
	report, err := e.discover(ctx)
	if err != nil {
		return report, err
	}
	report.DryRun = dryRun
	if dryRun {
		return report, nil
	}

	storage, err := e.Device.Storage(ctx)
	if err != nil {
		return report, fmt.Errorf("check Android storage: %w", err)
	}
	report.AvailableBytes = storage.AvailableBytes
	minimum := e.Config.Storage.MinFreeSpaceGB * gib
	if storage.AvailableBytes <= minimum {
		report.StorageLow = true
		e.log().Warn("Android storage below configured reserve", "available_bytes", storage.AvailableBytes, "minimum_bytes", minimum)
		return report, nil
	}

	batchID := fmt.Sprintf("%s-%s", e.now().UTC().Format("20060102T150405Z"), randomSuffix())
	if err := e.Store.CreateBatch(ctx, batchID); err != nil {
		return report, err
	}
	report.BatchID = batchID
	safeCapacity := storage.AvailableBytes - minimum
	batchLimit := e.Config.Storage.MaxBatchSizeGB * gib
	var batchBytes int64
	for {
		asset, err := e.Store.NextTransfer(ctx, e.now())
		if err != nil {
			return report, err
		}
		if asset == nil {
			break
		}
		if asset.ByteSize > 0 && (asset.ByteSize > safeCapacity || batchBytes+asset.ByteSize > batchLimit) {
			if err := e.Store.Requeue(ctx, asset.ID, "waiting for sufficient Android storage or next batch"); err != nil {
				return report, err
			}
			report.StorageLow = asset.ByteSize > safeCapacity
			break
		}
		outcome, bytes, err := e.transfer(ctx, *asset, batchID, safeCapacity-batchBytes)
		if err != nil {
			if markErr := e.Store.MarkFailed(ctx, asset.ID, asset.Attempts, e.Config.Sync.RetryAttempts, err); markErr != nil {
				return report, markErr
			}
			report.Failed++
			e.log().Warn("asset transfer failed", "asset_id", asset.ID, "attempt", asset.Attempts, "error", err)
			continue
		}
		if outcome == transferDeferred {
			if err := e.Store.Requeue(ctx, asset.ID, "waiting for sufficient Android storage or next batch"); err != nil {
				return report, err
			}
			report.StorageLow = true
			break
		}
		batchBytes += bytes
		report.Transferred++
	}
	return report, nil
}

func (e *Engine) discover(ctx context.Context) (Report, error) {
	var report Report
	_, initialized, err := e.Store.GetSetting(ctx, "initial_discovery_complete")
	if err != nil {
		return report, err
	}
	var after *time.Time
	initialIgnore := false
	if !initialized {
		switch e.Config.Sync.InitialMode {
		case "new-only":
			// The first full scan creates an explicit baseline. This prevents a
			// later reconciliation from unexpectedly uploading the old library.
			initialIgnore = true
		case "after-date":
			t, err := time.Parse(time.RFC3339, e.Config.Sync.InitialAfter)
			if err != nil {
				return report, fmt.Errorf("parse sync.initial_after: %w", err)
			}
			after = &t
		}
	} else if e.reconciliationDue(ctx) {
		e.log().Info("running full Immich reconciliation")
	} else if watermark, ok, err := e.Store.GetSetting(ctx, "last_discovery_at"); err != nil {
		return report, err
	} else if ok {
		t, err := time.Parse(time.RFC3339Nano, watermark)
		if err == nil {
			after = &t
		}
	}

	assets, err := e.Immich.ListAssets(ctx, after)
	if err != nil {
		return report, err
	}
	for _, asset := range assets {
		inserted, err := e.Store.UpsertDiscovered(ctx, asset, initialIgnore)
		if err != nil {
			return report, err
		}
		if !inserted {
			continue
		}
		report.Discovered++
		if initialIgnore {
			report.Skipped++
			continue
		}
		if !isAndroidMedia(asset.MIMEType, asset.OriginalName) {
			if err := e.Store.MarkUnsupported(ctx, asset.ID, "unsupported format: "+asset.MIMEType); err != nil {
				return report, err
			}
			report.Failed++
			continue
		}
		report.Queued++
	}
	stamp := e.now().UTC().Format(time.RFC3339Nano)
	if err := e.Store.SetSetting(ctx, "last_discovery_at", stamp); err != nil {
		return report, err
	}
	if !initialized {
		if err := e.Store.SetSetting(ctx, "initial_discovery_complete", "true"); err != nil {
			return report, err
		}
	}
	if after == nil {
		if err := e.Store.SetSetting(ctx, "last_reconciliation_at", stamp); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (e *Engine) reconciliationDue(ctx context.Context) bool {
	v, ok, err := e.Store.GetSetting(ctx, "last_reconciliation_at")
	if err != nil || !ok {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return true
	}
	interval, err := time.ParseDuration(e.Config.Sync.ReconciliationInterval)
	return err != nil || e.now().After(t.Add(interval))
}

type transferOutcome int

const (
	transferCompleted transferOutcome = iota
	transferDeferred
)

func (e *Engine) transfer(ctx context.Context, asset model.Asset, batchID string, available int64) (transferOutcome, int64, error) {
	path, sha, bytes, err := e.download(ctx, asset)
	if err != nil {
		return transferCompleted, 0, err
	}
	defer os.Remove(path)
	if bytes > available || bytes > e.Config.Storage.MaxBatchSizeGB*gib {
		return transferDeferred, 0, nil
	}
	destination, err := android.ManagedPath(e.Config.Android.Destination, asset.ID, asset.OriginalName)
	if err != nil {
		return transferCompleted, 0, err
	}
	if err := e.Device.EnsureDirectory(ctx, e.Config.Android.Destination); err != nil {
		return transferCompleted, 0, err
	}

	remoteSHA, supported, hashErr := e.Device.SHA256(ctx, destination)
	if hashErr == nil && supported && remoteSHA == sha {
		e.log().Info("verified existing Android file", "asset_id", asset.ID)
	} else {
		if hashErr != nil && !isMissingRemote(hashErr) {
			return transferCompleted, 0, hashErr
		}
		if !supported {
			if remoteSize, exists, err := e.Device.FileSize(ctx, destination); err != nil && !isMissingRemote(err) {
				return transferCompleted, 0, err
			} else if exists && remoteSize == bytes {
				e.log().Warn("accepting existing transfer using size-only verification; Android sha256sum is unavailable", "asset_id", asset.ID)
			} else if err := e.Device.PushAtomic(ctx, path, destination); err != nil {
				return transferCompleted, 0, err
			}
		} else if err := e.Device.PushAtomic(ctx, path, destination); err != nil {
			return transferCompleted, 0, err
		}
	}
	remoteSHA, supported, err = e.Device.SHA256(ctx, destination)
	if err != nil {
		return transferCompleted, 0, err
	}
	if supported && remoteSHA != sha {
		return transferCompleted, 0, fmt.Errorf("Android checksum mismatch")
	}
	if !supported {
		remoteSize, exists, err := e.Device.FileSize(ctx, destination)
		if err != nil || !exists || remoteSize != bytes {
			return transferCompleted, 0, errors.New("Android cannot hash the file and its size could not be verified")
		}
		e.log().Warn("transfer verified by file size only; Android sha256sum is unavailable", "asset_id", asset.ID)
	}
	if err := e.Device.ScanMedia(ctx, destination); err != nil {
		return transferCompleted, 0, err
	}
	if err := e.Store.MarkDelivered(ctx, asset.ID, destination, "", sha, batchID); err != nil {
		return transferCompleted, 0, err
	}
	return transferCompleted, bytes, nil
}

func (e *Engine) download(ctx context.Context, asset model.Asset) (string, string, int64, error) {
	if err := os.MkdirAll(e.CacheDir, 0o700); err != nil {
		return "", "", 0, err
	}
	partial, err := os.CreateTemp(e.CacheDir, asset.ID+"-*.partial")
	if err != nil {
		return "", "", 0, err
	}
	partialName := partial.Name()
	defer func() { partial.Close(); os.Remove(partialName) }()
	h := sha256.New()
	counter := &countingWriter{Writer: io.MultiWriter(partial, h)}
	err = e.Immich.DownloadOriginal(ctx, asset.ID, counter)
	if err != nil {
		return "", "", 0, err
	}
	n := counter.N
	if asset.ByteSize > 0 && n != asset.ByteSize {
		return "", "", 0, fmt.Errorf("downloaded %d bytes, expected %d", n, asset.ByteSize)
	}
	if err := partial.Sync(); err != nil {
		return "", "", 0, err
	}
	if err := partial.Close(); err != nil {
		return "", "", 0, err
	}
	finalPath := filepath.Join(e.CacheDir, asset.ID+"-ready")
	if err := os.Rename(partialName, finalPath); err != nil {
		return "", "", 0, err
	}
	return finalPath, hex.EncodeToString(h.Sum(nil)), n, nil
}

type countingWriter struct {
	io.Writer
	N int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.N += int64(n)
	return n, err
}

// CleanupBatch removes only database-tracked paths after an explicit user
// confirmation. It never sends an Immich mutation.
func (e *Engine) CleanupBatch(ctx context.Context, id string) (int, error) {
	if err := e.validate(); err != nil {
		return 0, err
	}
	assets, err := e.Store.ConfirmedPaths(ctx, id)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, asset := range assets {
		if !strings.HasPrefix(asset.DestinationPath, strings.TrimRight(e.Config.Android.Destination, "/")+"/") {
			return removed, fmt.Errorf("refusing to remove un-managed path for %s", asset.ID)
		}
		if err := e.Device.Remove(ctx, asset.DestinationPath); err != nil {
			return removed, err
		}
		if err := e.Store.MarkLocalRemoved(ctx, asset.ID); err != nil {
			return removed, err
		}
		removed++
	}
	if err := e.Store.MarkBatchCleaned(ctx, id); err != nil {
		return removed, err
	}
	return removed, nil
}

func (e *Engine) validate() error {
	if e.Store == nil || e.Immich == nil || e.Device == nil {
		return errors.New("sync engine dependencies are not configured")
	}
	if err := e.Config.Validate(); err != nil {
		return err
	}
	if e.CacheDir == "" {
		return errors.New("cache directory is required")
	}
	return nil
}
func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}
func isAndroidMedia(mime, name string) bool {
	if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	return map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".heif": true, ".dng": true, ".raw": true, ".cr2": true, ".nef": true, ".arw": true, ".mp4": true, ".mov": true, ".mkv": true}[ext]
}
func isMissingRemote(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "no such file") || strings.Contains(strings.ToLower(err.Error()), "not found")
}
func randomSuffix() string { return fmt.Sprintf("%x", time.Now().UnixNano()) }
