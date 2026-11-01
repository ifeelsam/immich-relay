package android

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestParseDevicesKeepsNoPermissionsAsOneState(t *testing.T) {
	devices := parseDevices("List of devices attached\na1862ff873f6    no permissions; see [http://developer.android.com/tools/device.html]\n")
	if len(devices) != 1 {
		t.Fatalf("devices = %d", len(devices))
	}
	if devices[0].State != "no permissions" {
		t.Fatalf("state = %q", devices[0].State)
	}
	if !strings.Contains(devices[0].Description, "permissions") {
		t.Fatalf("description = %q", devices[0].Description)
	}
	if err := deviceStateError(devices[0].Serial, devices[0].State); !strings.Contains(err.Error(), "udev") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateNetworkEndpoint(t *testing.T) {
	for _, endpoint := range []string{"192.168.1.20:34567", "phone.local:5555", "[fe80::1]:5555"} {
		if err := validateNetworkEndpoint(endpoint); err != nil {
			t.Fatalf("%q rejected: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"192.168.1.20", "phone:0", "phone:65536", "phone:not-a-port"} {
		if err := validateNetworkEndpoint(endpoint); err == nil {
			t.Fatalf("%q accepted", endpoint)
		}
	}
}

func TestResolveAutoSelectsTheOnlyAuthorizedDevice(t *testing.T) {
	dir := t.TempDir()
	adb := filepath.Join(dir, "adb")
	script := "#!/bin/sh\nprintf '%s\\n' 'List of devices attached' 'usb-device no permissions; see [https://developer.android.com/tools/adb]' '192.168.1.20:37123 device product:test'\n"
	if err := os.WriteFile(adb, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	serial, state, _, err := New(adb, "auto").resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if serial != "192.168.1.20:37123" || state != "device" {
		t.Fatalf("resolved %q in state %q", serial, state)
	}
}
