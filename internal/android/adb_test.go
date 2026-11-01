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
