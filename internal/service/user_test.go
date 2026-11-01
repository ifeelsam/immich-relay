package service

import (
	"strings"
	"testing"
)

func TestUnitContainsOnlyConfiguredArguments(t *testing.T) {
	unit := Unit("/usr/bin/immich-relay", "/home/me/.config/immich-relay/config.yaml", "/home/me/.local/state/immich-relay/relay.db", "adb")
	if !strings.Contains(unit, "ExecStart=\"/usr/bin/immich-relay\" run") || !strings.Contains(unit, "NoNewPrivileges=true") {
		t.Fatalf("unexpected unit: %s", unit)
	}
}
