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
