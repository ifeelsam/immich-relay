// Package config loads, validates, and persists the user's relay settings.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const AppName = "immich-relay"

type Config struct {
	Immich  ImmichConfig  `yaml:"immich"`
	Android AndroidConfig `yaml:"android"`
	Sync    SyncConfig    `yaml:"sync"`
	Storage StorageConfig `yaml:"storage"`
	Cleanup CleanupConfig `yaml:"cleanup"`
	Logging LoggingConfig `yaml:"logging"`
}

type ImmichConfig struct {
	URL        string `yaml:"url"`
	APIKeyFile string `yaml:"api_key_file"`
}

type AndroidConfig struct {
	DeviceSerial string `yaml:"device_serial"`
	Destination  string `yaml:"destination"`
}

type SyncConfig struct {
	Interval               string `yaml:"interval"`
	InitialMode            string `yaml:"initial_mode"`
	InitialAfter           string `yaml:"initial_after,omitempty"`
	TransferConcurrency    int    `yaml:"transfer_concurrency"`
	RetryAttempts          int    `yaml:"retry_attempts"`
	ReconciliationInterval string `yaml:"reconciliation_interval"`
}

type StorageConfig struct {
	MinFreeSpaceGB int64 `yaml:"min_free_space_gb"`
	MaxBatchSizeGB int64 `yaml:"max_batch_size_gb"`
	PauseWhenLow   bool  `yaml:"pause_when_low"`
}

type CleanupConfig struct {
	Strategy        string `yaml:"strategy"`
	UseSmartStorage bool   `yaml:"use_smart_storage"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

func Defaults() Config {
	return Config{
		Immich:  ImmichConfig{APIKeyFile: filepath.Join(ConfigDir(), "api-key")},
		Android: AndroidConfig{DeviceSerial: "auto", Destination: "/sdcard/DCIM/ImmichRelay"},
		Sync: SyncConfig{
			Interval: "5m", InitialMode: "new-only", TransferConcurrency: 1,
			RetryAttempts: 5, ReconciliationInterval: "24h",
		},
		Storage: StorageConfig{MinFreeSpaceGB: 20, MaxBatchSizeGB: 15, PauseWhenLow: true},
		Cleanup: CleanupConfig{Strategy: "manual-confirmation", UseSmartStorage: false},
		Logging: LoggingConfig{Level: "info"},
	}
}

func ConfigDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, AppName)
	}
	return filepath.Join(".", ".config", AppName)
}

func DefaultPath() string { return filepath.Join(ConfigDir(), "config.yaml") }
func DefaultDBPath() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, AppName, "relay.db")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", AppName, "relay.db")
	}
	return filepath.Join(ConfigDir(), "relay.db")
}
func CacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, AppName)
	}
	return filepath.Join(ConfigDir(), "cache")
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	c := Defaults()
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse configuration: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (c Config) Validate() error {
	if c.Immich.URL == "" {
		return errors.New("immich.url is required")
	}
	u, err := url.ParseRequestURI(c.Immich.URL)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("immich.url must be an absolute http(s) URL")
	}
	if c.Immich.APIKeyFile == "" {
		return errors.New("immich.api_key_file is required")
	}
	if !strings.HasPrefix(c.Android.Destination, "/sdcard/") || strings.ContainsAny(c.Android.Destination, "\n\r'\"`;") {
		return errors.New("android.destination must be a safe absolute path below /sdcard")
	}
	if c.Sync.TransferConcurrency != 1 {
		return errors.New("sync.transfer_concurrency must be 1 in this release to respect phone storage limits")
	}
	if _, err := time.ParseDuration(c.Sync.Interval); err != nil {
		return fmt.Errorf("sync.interval: %w", err)
	}
	if _, err := time.ParseDuration(c.Sync.ReconciliationInterval); err != nil {
		return fmt.Errorf("sync.reconciliation_interval: %w", err)
	}
	if c.Sync.RetryAttempts < 1 {
		return errors.New("sync.retry_attempts must be at least 1")
	}
	if c.Storage.MinFreeSpaceGB < 0 || c.Storage.MaxBatchSizeGB < 1 {
		return errors.New("storage limits must be non-negative and max_batch_size_gb must be positive")
	}
	if c.Cleanup.Strategy != "manual-confirmation" {
		return errors.New("cleanup.strategy must be manual-confirmation; automatic Google Photos verification is not supported")
	}
	for _, mode := range []string{"new-only", "full", "after-date"} {
		if c.Sync.InitialMode == mode {
			if mode == "after-date" && c.Sync.InitialAfter == "" {
				return errors.New("sync.initial_after is required when initial_mode is after-date")
			}
			return nil
		}
	}
	return errors.New("sync.initial_mode must be new-only, full, or after-date")
}

func ReadAPIKey(path string) (string, error) {
	path = ExpandPath(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Immich API key: %w", err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", errors.New("Immich API key is empty")
	}
	return key, nil
}

func WriteAPIKey(path, key string) error {
	path = ExpandPath(path)
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("Immich API key is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(key+"\n"), 0o600)
}

// ExpandPath supports the common configuration convention for a home-relative
// credential path without making arbitrary shell expansion part of config.
func ExpandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
