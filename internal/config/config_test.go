package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialPathExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ExpandPath("~/x"), filepath.Join(home, "x"); got != want {
		t.Fatalf("ExpandPath = %q, want %q", got, want)
	}
}

func TestConfigRequiresImmichURL(t *testing.T) {
	c := Defaults()
	if err := c.Validate(); err == nil {
		t.Fatal("expected missing URL error")
	}
}

func TestConfigAllowsAndroidInternalStorageAlias(t *testing.T) {
	c := Defaults()
	c.Immich.URL = "https://photos.example.test"
	c.Android.Destination = "/storage/emulated/0/DCIM/ImmichRelay"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAPIKeyUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	if err := WriteAPIKey(path, "secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
}
