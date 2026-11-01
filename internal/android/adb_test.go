package android

import "testing"

func TestManagedPathIsCollisionSafeAndRejectsUnsafeIDs(t *testing.T) {
	a, err := ManagedPath("/sdcard/DCIM/ImmichRelay", "asset-one", "trip.jpg")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ManagedPath("/sdcard/DCIM/ImmichRelay", "asset-two", "trip.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("paths collide: %q", a)
	}
	if _, err := ManagedPath("/sdcard/DCIM/ImmichRelay", "../../bad", "trip.jpg"); err == nil {
		t.Fatal("unsafe asset id accepted")
	}
}

func TestManagedPathSupportsAndroidInternalStorageAlias(t *testing.T) {
	p, err := ManagedPath("/storage/emulated/0/DCIM/ImmichRelay", "asset-one", "trip.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/storage/emulated/0/DCIM/ImmichRelay/asset-one-trip.jpg" {
		t.Fatalf("path = %q", p)
	}
	if _, err := ManagedPath("/storage/emulated/0/DCIM/ImmichRelay/../../data", "asset-one", "trip.jpg"); err == nil {
		t.Fatal("path traversal accepted")
	}
}
