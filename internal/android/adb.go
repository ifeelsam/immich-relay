// Package android provides a deliberately small, shell-safe ADB integration.
package android

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/immich-relay/immich-relay/internal/model"
)

var serialPattern = regexp.MustCompile(`^[A-Za-z0-9:._\-\[\]]+$`)

// Device is the Android boundary used by the engine; it is straightforward to
// fake in tests and never needs a real phone.
type Device interface {
	Info(context.Context) (Info, error)
	Storage(context.Context) (model.StorageInfo, error)
	EnsureDirectory(context.Context, string) error
	PushAtomic(context.Context, string, string) error
	SHA256(context.Context, string) (sum string, supported bool, err error)
	FileSize(context.Context, string) (size int64, exists bool, err error)
	ScanMedia(context.Context, string) error
	Remove(context.Context, string) error
}

type Info struct {
	Serial      string
	State       string
	Model       string
	Android     string
	Authorized  bool
	Description string
}

type ADB struct {
	Binary string
	Serial string
}

func New(binary, serial string) *ADB {
	if binary == "" {
		binary = "adb"
	}
	return &ADB{Binary: binary, Serial: serial}
}

func (a *ADB) Info(ctx context.Context) (Info, error) {
	serial, state, description, err := a.resolve(ctx)
	if err != nil {
		return Info{}, err
	}
	info := Info{Serial: serial, State: state, Authorized: state == "device", Description: description}
	if !info.Authorized {
		return info, deviceStateError(serial, state)
	}
	model, _ := a.shell(ctx, "getprop ro.product.model")
	version, _ := a.shell(ctx, "getprop ro.build.version.release")
	info.Model, info.Android = strings.TrimSpace(model), strings.TrimSpace(version)
	return info, nil
}

// Connect asks the local ADB server to connect to a phone already paired for
// wireless debugging. Pairing is deliberately left to `adb pair`, which keeps
// the short-lived pairing code out of Relay configuration and process logs.
func (a *ADB) Connect(ctx context.Context, endpoint string) error {
	if err := validateNetworkEndpoint(endpoint); err != nil {
		return err
	}
	if _, err := exec.CommandContext(ctx, a.Binary, "connect", endpoint).CombinedOutput(); err != nil {
		return fmt.Errorf("connect to Android device %s: %w", endpoint, err)
	}
	return nil
}

func (a *ADB) Storage(ctx context.Context) (model.StorageInfo, error) {
	out, err := a.shell(ctx, "df -k /sdcard")
	if err != nil {
		return model.StorageInfo{}, fmt.Errorf("query Android storage: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		total, e1 := strconv.ParseInt(fields[1], 10, 64)
		available, e2 := strconv.ParseInt(fields[3], 10, 64)
		if e1 == nil && e2 == nil {
			return model.StorageInfo{TotalBytes: total * 1024, AvailableBytes: available * 1024}, nil
		}
	}
	return model.StorageInfo{}, errors.New("could not parse df output from Android")
}

func (a *ADB) EnsureDirectory(ctx context.Context, destination string) error {
	if err := validateRemote(destination); err != nil {
		return err
	}
	_, err := a.shell(ctx, "mkdir -p "+shellQuote(destination))
	return err
}

func (a *ADB) PushAtomic(ctx context.Context, local, destination string) error {
	if err := validateRemote(destination); err != nil {
		return err
	}
	temporary := destination + ".immich-relay-partial"
	if _, err := a.command(ctx, "push", local, temporary); err != nil {
		return fmt.Errorf("adb push: %w", err)
	}
	// -n protects a user-created collision. The engine verifies the resulting
	// destination afterwards, so a failed no-clobber rename cannot be accepted.
	if _, err := a.shell(ctx, "mv -n "+shellQuote(temporary)+" "+shellQuote(destination)+"; status=$?; rm -f -- "+shellQuote(temporary)+"; exit $status"); err != nil {
		return fmt.Errorf("finalize Android transfer: %w", err)
	}
	return nil
}

func (a *ADB) SHA256(ctx context.Context, remote string) (string, bool, error) {
	if err := validateRemote(remote); err != nil {
		return "", false, err
	}
	out, err := a.shell(ctx, "if command -v sha256sum >/dev/null 2>&1; then sha256sum "+shellQuote(remote)+"; elif command -v toybox >/dev/null 2>&1 && toybox sha256sum /dev/null >/dev/null 2>&1; then toybox sha256sum "+shellQuote(remote)+"; else printf IMMICH_RELAY_HASH_UNAVAILABLE; fi")
	if err != nil {
		return "", false, err
	}
	out = strings.TrimSpace(out)
	if out == "IMMICH_RELAY_HASH_UNAVAILABLE" {
		return "", false, nil
	}
	fields := strings.Fields(out)
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", false, fmt.Errorf("invalid Android SHA-256 output")
	}
	return strings.ToLower(fields[0]), true, nil
}

// FileSize is the explicit, documented fallback for Android builds without a
// SHA-256 utility. A matching size is weaker than a checksum and is logged by
// the engine so users can see that limitation.
func (a *ADB) FileSize(ctx context.Context, remote string) (int64, bool, error) {
	if err := validateRemote(remote); err != nil {
		return 0, false, err
	}
	out, err := a.shell(ctx, "if [ -f "+shellQuote(remote)+" ]; then wc -c < "+shellQuote(remote)+"; else printf IMMICH_RELAY_MISSING; fi")
	if err != nil {
		return 0, false, err
	}
	out = strings.TrimSpace(out)
	if out == "IMMICH_RELAY_MISSING" {
		return 0, false, nil
	}
	size, err := strconv.ParseInt(out, 10, 64)
	if err != nil || size < 0 {
		return 0, false, fmt.Errorf("invalid Android file size output")
	}
	return size, true, nil
}

func (a *ADB) ScanMedia(ctx context.Context, remote string) error {
	if err := validateRemote(remote); err != nil {
		return err
	}
	_, err := a.shell(ctx, "am broadcast --user 0 -a android.intent.action.MEDIA_SCANNER_SCAN_FILE -d "+shellQuote("file://"+remote))
	if err != nil {
		return fmt.Errorf("request Android MediaStore scan: %w", err)
	}
	return nil
}

func (a *ADB) Remove(ctx context.Context, remote string) error {
	if err := validateRemote(remote); err != nil {
		return err
	}
	_, err := a.shell(ctx, "rm -f -- "+shellQuote(remote))
	return err
}

func (a *ADB) resolve(ctx context.Context) (string, string, string, error) {
	out, err := exec.CommandContext(ctx, a.Binary, "devices", "-l").CombinedOutput()
	if err != nil {
		return "", "", "", fmt.Errorf("run adb devices: %w", err)
	}
	devices := parseDevices(string(out))
	if a.Serial != "" && a.Serial != "auto" {
		if !serialPattern.MatchString(a.Serial) {
			return "", "", "", errors.New("invalid ADB device serial")
		}
		for _, d := range devices {
			if d.Serial == a.Serial {
				return d.Serial, d.State, d.Description, nil
			}
		}
		return "", "", "", fmt.Errorf("configured Android device %s is not connected", a.Serial)
	}
	if len(devices) == 0 {
		return "", "", "", errors.New("no Android devices found; connect a device and enable USB debugging")
	}
	var authorized []Info
	for _, device := range devices {
		if device.State == "device" {
			authorized = append(authorized, device)
		}
	}
	if len(authorized) == 1 {
		return authorized[0].Serial, authorized[0].State, authorized[0].Description, nil
	}
	if len(authorized) > 1 {
		return "", "", "", errors.New("more than one authorized Android device is connected; set android.device_serial")
	}
	if len(devices) != 1 {
		return "", "", "", errors.New("no authorized Android device is connected; set android.device_serial after connecting or authorizing one")
	}
	return devices[0].Serial, devices[0].State, devices[0].Description, nil
}

func (a *ADB) command(ctx context.Context, args ...string) (string, error) {
	serial, state, _, err := a.resolve(ctx)
	if err != nil {
		return "", err
	}
	if state != "device" {
		return "", deviceStateError(serial, state)
	}
	args = append([]string{"-s", serial}, args...)
	out, err := exec.CommandContext(ctx, a.Binary, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func parseDevices(output string) []Info {
	var devices []Info
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "List") {
			continue
		}
		state, description := fields[1], strings.Join(fields[2:], " ")
		// `adb devices` represents this host-side USB/udev failure as two
		// columns: "no permissions; ...". Keep it as one actionable state.
		if state == "no" && len(fields) >= 3 && strings.HasPrefix(fields[2], "permissions") {
			state, description = "no permissions", strings.Join(fields[2:], " ")
		}
		devices = append(devices, Info{Serial: fields[0], State: state, Description: description})
	}
	return devices
}

func deviceStateError(serial, state string) error {
	switch state {
	case "no permissions":
		return fmt.Errorf("Android device %s is visible, but this Linux user cannot access it; install your distribution's Android USB udev rules and reconnect, or use wireless debugging", serial)
	case "unauthorized":
		return fmt.Errorf("Android device %s is unauthorized; unlock it and accept the USB debugging authorization", serial)
	case "offline":
		return fmt.Errorf("Android device %s is offline; reconnect it or reconnect ADB", serial)
	default:
		return fmt.Errorf("Android device %s is %s; authorize USB debugging", serial, state)
	}
}

func validateNetworkEndpoint(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return errors.New("wireless Android address must be host:port")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return errors.New("wireless Android port must be between 1 and 65535")
	}
	return nil
}

func (a *ADB) shell(ctx context.Context, command string) (string, error) {
	return a.command(ctx, "shell", command)
}

func validateRemote(value string) error {
	if strings.ContainsAny(value, "\n\r'\"`;$&|<>()") {
		return errors.New("unsafe Android path")
	}
	clean := path.Clean(value)
	if strings.TrimRight(value, "/") != clean || (!strings.HasPrefix(clean, "/sdcard/") && !strings.HasPrefix(clean, "/storage/emulated/0/")) {
		return errors.New("Android path must be below /sdcard or /storage/emulated/0")
	}
	return nil
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// ManagedPath validates and constructs a collision-safe path. Including the
// immutable asset ID avoids overwriting media that merely shares a filename.
func ManagedPath(destination, assetID, originalName string) (string, error) {
	if err := validateRemote(destination); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(assetID) {
		return "", errors.New("unsafe Immich asset id")
	}
	base := path.Base(originalName)
	base = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, base)
	if base == "." || base == "" {
		base = "asset"
	}
	return strings.TrimRight(destination, "/") + "/" + assetID + "-" + base, nil
}
