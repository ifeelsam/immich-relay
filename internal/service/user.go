// Package service installs the optional systemd user unit used by the daemon.
package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const UnitName = "immich-relay.service"

func Unit(binary, configPath, dbPath, adbBinary string) string {
	return fmt.Sprintf(`[Unit]
Description=Immich Relay Android sync daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run --config %s --db %s --adb %s
Restart=on-failure
RestartSec=15
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=default.target
`, quote(binary), quote(configPath), quote(dbPath), quote(adbBinary))
}

func UserUnitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "systemd", "user", UnitName), nil
}

func InstallUser(ctx context.Context, binary, configPath, dbPath, adbBinary string) (string, error) {
	path, err := UserUnitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(Unit(binary, configPath, dbPath, adbBinary)), 0o644); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", UnitName}} {
		output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
		if err != nil {
			return path, fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	return path, nil
}

func quote(value string) string {
	// systemd command lines use POSIX-style quoting. The paths originate from
	// local configuration and we reject newlines, which would alter the unit.
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
