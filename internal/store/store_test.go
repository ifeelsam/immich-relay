package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/immich-relay/immich-relay/internal/model"
	"github.com/immich-relay/immich-relay/internal/store"
)

func TestQueuePersistsAndRecoversInterruptedTransfer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpsertDiscovered(ctx, model.Asset{ID: "a1", OriginalName: "one.jpg", MIMEType: "image/jpeg", CreatedAt: time.Now()}, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.NextTransfer(ctx, time.Now())
	if err != nil || a == nil {
		t.Fatalf("NextTransfer() = %v, %v", a, err)
	}
	if a.State != model.StateTransferring {
		t.Fatalf("state = %s", a.State)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err = s.NextTransfer(ctx, time.Now())
	if err != nil || a == nil {
		t.Fatalf("recovered NextTransfer() = %v, %v", a, err)
	}
	if a.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", a.Attempts)
	}
}

func TestConfirmationAndCleanupStateTransitions(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBatch(ctx, "batch-1"); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpsertDiscovered(ctx, model.Asset{ID: "a1", OriginalName: "one.jpg", MIMEType: "image/jpeg", CreatedAt: time.Now()}, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.NextTransfer(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkDelivered(ctx, a.ID, "/sdcard/DCIM/ImmichRelay/a1-one.jpg", "", "abc", "batch-1"); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmBatch(ctx, "batch-1"); err != nil {
		t.Fatal(err)
	}
	paths, err := s.ConfirmedPaths(ctx, "batch-1")
	if err != nil || len(paths) != 1 {
		t.Fatalf("ConfirmedPaths = %v, %v", paths, err)
	}
	if err = s.MarkLocalRemoved(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	assets, err := s.List(ctx, string(model.StateLocalRemoved))
	if err != nil || len(assets) != 1 {
		t.Fatalf("List = %v, %v", assets, err)
	}
}
