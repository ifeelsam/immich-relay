package syncer_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/immich-relay/immich-relay/internal/android"
	"github.com/immich-relay/immich-relay/internal/config"
	"github.com/immich-relay/immich-relay/internal/model"
	"github.com/immich-relay/immich-relay/internal/store"
	"github.com/immich-relay/immich-relay/internal/syncer"
)

func TestRunOnceTransfersVerifiesScansAndCleansConfirmedBatch(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := &fakeSource{assets: []model.Asset{asset("photo-1", "holiday.jpg", "image/jpeg", 3)}, data: map[string][]byte{"photo-1": []byte("abc")}}
	device := newFakeDevice(80 * gib)
	e := newEngine(t, s, source, device)
	report, err := e.RunOnce(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Transferred != 1 || report.BatchID == "" {
		t.Fatalf("report = %+v", report)
	}
	if len(device.files) != 1 || len(device.scanned) != 1 {
		t.Fatalf("device state files=%d scans=%d", len(device.files), len(device.scanned))
	}
	awaiting, err := s.List(ctx, string(model.StateAwaitingBackupConfirmation))
	if err != nil || len(awaiting) != 1 {
		t.Fatalf("awaiting = %#v, %v", awaiting, err)
	}
	if err = s.ConfirmBatch(ctx, report.BatchID); err != nil {
		t.Fatal(err)
	}
	removed, err := e.CleanupBatch(ctx, report.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(device.files) != 0 {
		t.Fatalf("removed=%d remaining=%d", removed, len(device.files))
	}
	localRemoved, err := s.List(ctx, string(model.StateLocalRemoved))
	if err != nil || len(localRemoved) != 1 {
		t.Fatalf("local removed = %#v, %v", localRemoved, err)
	}
}

func TestNewOnlyMakesAnExplicitBaseline(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := &fakeSource{assets: []model.Asset{asset("old-1", "old.jpg", "image/jpeg", 3)}, data: map[string][]byte{"old-1": []byte("abc")}}
	e := newEngine(t, s, source, newFakeDevice(80*gib))
	e.Config.Sync.InitialMode = "new-only"
	report, err := e.RunOnce(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Transferred != 0 || report.Skipped != 1 {
		t.Fatalf("report = %+v", report)
	}
	ignored, err := s.List(ctx, string(model.StateIgnored))
	if err != nil || len(ignored) != 1 {
		t.Fatalf("ignored=%v err=%v", ignored, err)
	}
}

func TestLowStorageKeepsQueuedAssetsDurably(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := newEngine(t, s, &fakeSource{assets: []model.Asset{asset("a1", "a.jpg", "image/jpeg", 3)}, data: map[string][]byte{"a1": []byte("abc")}}, newFakeDevice(20*gib))
	report, err := e.RunOnce(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.StorageLow || report.Transferred != 0 {
		t.Fatalf("report = %+v", report)
	}
	queued, err := s.List(ctx, string(model.StateQueued))
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued = %#v, %v", queued, err)
	}
}

func TestInterruptedDownloadIsRetriedWithoutFalseDelivery(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source := &fakeSource{assets: []model.Asset{asset("a1", "a.jpg", "image/jpeg", 3)}, data: map[string][]byte{"a1": []byte("abc")}, downloadErr: errors.New("network interrupted")}
	e := newEngine(t, s, source, newFakeDevice(80*gib))
	report, err := e.RunOnce(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 {
		t.Fatalf("report = %+v", report)
	}
	failed, err := s.List(ctx, string(model.StateFailed))
	if err != nil || len(failed) != 1 || failed[0].Attempts != 1 {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
}

const gib = int64(1024 * 1024 * 1024)

func asset(id, name, mime string, size int64) model.Asset {
	return model.Asset{ID: id, OriginalName: name, MIMEType: mime, ByteSize: size, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func newEngine(t *testing.T, s *store.Store, source *fakeSource, device *fakeDevice) *syncer.Engine {
	t.Helper()
	c := config.Defaults()
	c.Immich.URL = "https://immich.example.test"
	c.Sync.InitialMode = "full"
	c.Storage.MinFreeSpaceGB = 20
	c.Storage.MaxBatchSizeGB = 15
	return &syncer.Engine{Config: c, Store: s, Immich: source, Device: device, CacheDir: filepath.Join(t.TempDir(), "cache"), Now: func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }}
}

type fakeSource struct {
	assets      []model.Asset
	data        map[string][]byte
	downloadErr error
}

func (f *fakeSource) Version(context.Context) (string, error) { return "test", nil }
func (f *fakeSource) ListAssets(context.Context, *time.Time) ([]model.Asset, error) {
	return f.assets, nil
}
func (f *fakeSource) DownloadOriginal(_ context.Context, id string, w io.Writer) error {
	if f.downloadErr != nil {
		return f.downloadErr
	}
	_, err := w.Write(f.data[id])
	return err
}

type fakeDevice struct {
	storage model.StorageInfo
	files   map[string][]byte
	scanned []string
}

func newFakeDevice(available int64) *fakeDevice {
	return &fakeDevice{storage: model.StorageInfo{AvailableBytes: available, TotalBytes: 128 * gib}, files: map[string][]byte{}}
}
func (f *fakeDevice) Info(context.Context) (android.Info, error) {
	return android.Info{Serial: "test", Authorized: true}, nil
}
func (f *fakeDevice) Storage(context.Context) (model.StorageInfo, error) { return f.storage, nil }
func (f *fakeDevice) EnsureDirectory(context.Context, string) error      { return nil }
func (f *fakeDevice) PushAtomic(_ context.Context, local, destination string) error {
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	if _, exists := f.files[destination]; exists {
		return errors.New("collision")
	}
	f.files[destination] = b
	return nil
}
func (f *fakeDevice) SHA256(_ context.Context, remote string) (string, bool, error) {
	b, ok := f.files[remote]
	if !ok {
		return "", true, errors.New("no such file")
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), true, nil
}
func (f *fakeDevice) FileSize(_ context.Context, remote string) (int64, bool, error) {
	b, ok := f.files[remote]
	return int64(len(b)), ok, nil
}
func (f *fakeDevice) ScanMedia(_ context.Context, remote string) error {
	if _, ok := f.files[remote]; !ok {
		return errors.New("missing")
	}
	f.scanned = append(f.scanned, remote)
	return nil
}
func (f *fakeDevice) Remove(_ context.Context, remote string) error {
	delete(f.files, remote)
	return nil
}
