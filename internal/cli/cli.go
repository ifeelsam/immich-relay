// Package cli exposes a small, scriptable command line around the relay.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/immich-relay/immich-relay/internal/android"
	"github.com/immich-relay/immich-relay/internal/config"
	"github.com/immich-relay/immich-relay/internal/immich"
	"github.com/immich-relay/immich-relay/internal/model"
	"github.com/immich-relay/immich-relay/internal/service"
	"github.com/immich-relay/immich-relay/internal/store"
	"github.com/immich-relay/immich-relay/internal/syncer"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type options struct {
	configPath string
	dbPath     string
	adbBinary  string
	logLevel   string
}

type runtime struct {
	config config.Config
	store  *store.Store
	client *immich.Client
	device *android.ADB
	engine *syncer.Engine
}

func New(version string) *cobra.Command {
	o := options{configPath: config.DefaultPath(), dbPath: config.DefaultDBPath(), adbBinary: "adb", logLevel: "info"}
	root := &cobra.Command{
		Use:           "immich-relay",
		Short:         "Safely relay Immich originals to an Android Google Photos gateway",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&o.configPath, "config", o.configPath, "configuration file path")
	root.PersistentFlags().StringVar(&o.dbPath, "db", o.dbPath, "SQLite queue database path")
	root.PersistentFlags().StringVar(&o.adbBinary, "adb", o.adbBinary, "ADB executable")
	root.PersistentFlags().StringVar(&o.logLevel, "log-level", o.logLevel, "log level: debug, info, warn, error")
	root.AddCommand(setupCommand(&o), doctorCommand(&o), statusCommand(&o), syncCommand(&o), runCommand(&o), queueCommand(&o), batchCommand(&o), deviceCommand(&o), serviceCommand(&o))
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print build information", Run: func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) }})
	return root
}

func setupCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use: "setup", Short: "Run the first-time setup wizard",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			in := bufio.NewReader(cmd.InOrStdin())
			fmt.Fprintln(out, "Immich Relay setup")
			fmt.Fprintln(out, "Immich Relay sends original files to Android; Google Photos backup must be configured separately.")
			c := config.Defaults()
			c.Immich.APIKeyFile = filepath.Join(filepath.Dir(o.configPath), "api-key")
			var err error
			if c.Immich.URL, err = ask(in, out, "Immich server URL", c.Immich.URL); err != nil {
				return err
			}
			apiKey, err := askSecret(cmd.InOrStdin(), in, out, "Immich API key")
			if err != nil {
				return err
			}
			if c.Android.DeviceSerial, err = ask(in, out, "Android serial (auto selects exactly one authorized device)", c.Android.DeviceSerial); err != nil {
				return err
			}
			if c.Android.Destination, err = ask(in, out, "Android destination", c.Android.Destination); err != nil {
				return err
			}
			if c.Sync.InitialMode, err = ask(in, out, "Initial sync mode (new-only, full, after-date)", c.Sync.InitialMode); err != nil {
				return err
			}
			if c.Sync.InitialMode == "after-date" {
				if c.Sync.InitialAfter, err = ask(in, out, "First asset timestamp (RFC3339)", ""); err != nil {
					return err
				}
			}
			if c.Storage.MinFreeSpaceGB, err = askInt(in, out, "Minimum free Android storage (GB)", c.Storage.MinFreeSpaceGB); err != nil {
				return err
			}
			if c.Sync.Interval, err = ask(in, out, "Sync interval", c.Sync.Interval); err != nil {
				return err
			}
			if err := c.Validate(); err != nil {
				return err
			}

			client, err := immich.New(c.Immich.URL, apiKey)
			if err != nil {
				return err
			}
			if version, err := client.Version(cmd.Context()); err != nil {
				return fmt.Errorf("Immich connection check failed: %w", err)
			} else {
				fmt.Fprintf(out, "✓ Immich connection successful (server %s)\n", version)
			}
			device := android.New(o.adbBinary, c.Android.DeviceSerial)
			info, deviceErr := device.Info(cmd.Context())
			if deviceErr != nil {
				return fmt.Errorf("Android device check failed: %w", deviceErr)
			}
			storage, err := device.Storage(cmd.Context())
			if err != nil {
				return fmt.Errorf("Android storage check failed: %w", err)
			}
			fmt.Fprintf(out, "✓ Android %s (%s, Android %s), %.1f GB available\n", info.Serial, info.Model, info.Android, float64(storage.AvailableBytes)/(1024*1024*1024))
			if err := config.WriteAPIKey(c.Immich.APIKeyFile, apiKey); err != nil {
				return err
			}
			if err := c.Save(o.configPath); err != nil {
				return err
			}
			db, err := store.Open(o.dbPath)
			if err != nil {
				return fmt.Errorf("initialize sync database: %w", err)
			}
			if err := db.Close(); err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ Saved configuration to %s\n", o.configPath)
			fmt.Fprintln(out, "Before syncing, configure Google Photos to back up the ImmichRelay folder. Relay cannot verify that Google Photos uploaded an individual file.")
			enable, err := askYesNo(in, out, "Enable the systemd user service now", true)
			if err != nil {
				return err
			}
			if enable {
				binary, err := os.Executable()
				if err != nil {
					return err
				}
				path, err := service.InstallUser(cmd.Context(), binary, o.configPath, o.dbPath, o.adbBinary)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "✓ Enabled %s\n", path)
			}
			return nil
		}}
}

func doctorCommand(o *options) *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Check configuration, Immich, ADB, and Android readiness", RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		c, err := config.Load(o.configPath)
		if err != nil {
			return fmt.Errorf("configuration: %w", err)
		}
		fmt.Fprintf(out, "✓ Configuration: %s\n", o.configPath)
		if _, err := os.Stat(c.Immich.APIKeyFile); err != nil {
			return fmt.Errorf("API key file: %w", err)
		}
		fmt.Fprintln(out, "✓ API key file is present")
		if _, err := exec.LookPath(o.adbBinary); err != nil {
			return fmt.Errorf("ADB: %w", err)
		}
		fmt.Fprintf(out, "✓ ADB: %s\n", o.adbBinary)
		key, err := config.ReadAPIKey(c.Immich.APIKeyFile)
		if err != nil {
			return err
		}
		client, err := immich.New(c.Immich.URL, key)
		if err != nil {
			return err
		}
		version, err := client.Version(cmd.Context())
		if err != nil {
			return fmt.Errorf("Immich: %w", err)
		}
		fmt.Fprintf(out, "✓ Immich server: %s\n", version)
		device := android.New(o.adbBinary, c.Android.DeviceSerial)
		info, err := device.Info(cmd.Context())
		if err != nil {
			return err
		}
		storage, err := device.Storage(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ Android: %s (%s, Android %s), %.1f GB free\n", info.Serial, info.Model, info.Android, float64(storage.AvailableBytes)/(1024*1024*1024))
		if _, err := os.Stat(o.dbPath); err != nil {
			fmt.Fprintf(out, "! Queue database does not exist yet: %s\n", o.dbPath)
		} else {
			fmt.Fprintf(out, "✓ Queue database: %s\n", o.dbPath)
		}
		if state, err := service.UserStatus(cmd.Context()); err != nil {
			fmt.Fprintf(out, "! systemd user service: %s (%v)\n", state, err)
		} else {
			fmt.Fprintf(out, "✓ systemd user service: %s\n", state)
		}
		return nil
	}}
}

func statusCommand(o *options) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show durable queue and device status", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := openRuntime(cmd.Context(), o)
		if err != nil {
			return err
		}
		defer r.store.Close()
		out := cmd.OutOrStdout()
		counts, err := r.store.Counts(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Queue:")
		for _, state := range []model.State{model.StateQueued, model.StateTransferring, model.StateAwaitingBackupConfirmation, model.StateBackupConfirmed, model.StateFailed, model.StateLocalRemoved, model.StateIgnored} {
			fmt.Fprintf(out, "  %-28s %d\n", state, counts[state])
		}
		batches, err := r.store.ListBatches(cmd.Context())
		if err != nil {
			return err
		}
		outstanding := 0
		for _, b := range batches {
			if b.ConfirmedAt == nil {
				outstanding++
			}
		}
		fmt.Fprintf(out, "Unconfirmed batches: %d\n", outstanding)
		if stamp, ok, err := r.store.GetSetting(cmd.Context(), "last_successful_sync_at"); err != nil {
			return err
		} else if ok {
			fmt.Fprintf(out, "Last successful sync pass: %s\n", stamp)
		} else {
			fmt.Fprintln(out, "Last successful sync pass: never")
		}
		if storage, e := r.device.Storage(cmd.Context()); e != nil {
			fmt.Fprintf(out, "Android: unavailable (%v)\n", e)
		} else {
			fmt.Fprintf(out, "Android free space: %.1f GB\n", float64(storage.AvailableBytes)/(1024*1024*1024))
		}
		return nil
	}}
}

func syncCommand(o *options) *cobra.Command {
	var once bool
	var dryRun bool
	c := &cobra.Command{Use: "sync --once", Short: "Run a single synchronization pass", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !once {
			return errors.New("sync requires --once; use run for the daemon")
		}
		r, err := openRuntime(cmd.Context(), o)
		if err != nil {
			return err
		}
		defer r.store.Close()
		report, err := r.engine.RunOnce(cmd.Context(), dryRun)
		if err != nil {
			return err
		}
		printReport(cmd.OutOrStdout(), report)
		return nil
	}}
	c.Flags().BoolVar(&once, "once", false, "run one synchronization pass")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "discover and queue assets without transferring files")
	return c
}

func runCommand(o *options) *cobra.Command {
	return &cobra.Command{Use: "run", Short: "Run the continuous synchronization daemon", RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := openRuntime(cmd.Context(), o)
		if err != nil {
			return err
		}
		defer r.store.Close()
		interval, err := time.ParseDuration(r.config.Sync.Interval)
		if err != nil {
			return err
		}
		logger := loggerFor(o.logLevel)
		for {
			report, runErr := r.engine.RunOnce(cmd.Context(), false)
			if runErr != nil {
				logger.Error("sync pass failed", "error", runErr)
			} else {
				logger.Info("sync pass complete", "transferred", report.Transferred, "failed", report.Failed, "storage_low", report.StorageLow)
			}
			timer := time.NewTimer(interval)
			select {
			case <-cmd.Context().Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}}
}

func queueCommand(o *options) *cobra.Command {
	q := &cobra.Command{Use: "queue", Short: "Inspect or retry persistent work"}
	var state string
	q.AddCommand(&cobra.Command{Use: "list", Short: "List queued assets", RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := openStore(o.dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		assets, err := s.List(cmd.Context(), state)
		if err != nil {
			return err
		}
		for _, a := range assets {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tattempts=%d\t%s\n", a.ID, a.State, a.OriginalName, a.Attempts, a.LastError)
		}
		return nil
	}})
	q.Commands()[0].Flags().StringVar(&state, "state", "", "filter by state")
	q.AddCommand(&cobra.Command{Use: "retry", Short: "Make failed assets eligible for retry", RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := openStore(o.dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		n, err := s.RetryFailed(cmd.Context())
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Requeued %d assets\n", n)
		}
		return err
	}})
	q.AddCommand(&cobra.Command{Use: "requeue <asset-id>", Short: "Explicitly retransmit a previously processed asset", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore(o.dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		if err := s.RequeueAsset(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Asset requeued for an explicit retransmission.")
		return nil
	}})
	return q
}

func batchCommand(o *options) *cobra.Command {
	b := &cobra.Command{Use: "batch", Short: "Manage explicit Google Photos confirmation batches"}
	b.AddCommand(&cobra.Command{Use: "list", Short: "List batches", RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := openStore(o.dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		batches, err := s.ListBatches(cmd.Context())
		if err != nil {
			return err
		}
		for _, batch := range batches {
			confirmed := "no"
			if batch.ConfirmedAt != nil {
				confirmed = "yes"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\tcreated=%s\tconfirmed=%s\n", batch.ID, batch.CreatedAt.Format(time.RFC3339), confirmed)
		}
		return nil
	}})
	b.AddCommand(&cobra.Command{Use: "confirm <batch-id>", Short: "Record explicit user confirmation of Google Photos backup", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore(o.dbPath)
		if err != nil {
			return err
		}
		defer s.Close()
		if err := s.ConfirmBatch(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Batch confirmed. You may now run batch cleanup if you want to remove relay-managed phone copies.")
		return nil
	}})
	b.AddCommand(&cobra.Command{Use: "cleanup <batch-id>", Short: "Remove only confirmed, relay-managed Android files", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, err := openCleanupRuntime(cmd.Context(), o)
		if err != nil {
			return err
		}
		defer r.store.Close()
		n, err := r.engine.CleanupBatch(cmd.Context(), args[0])
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %d confirmed Android files\n", n)
		}
		return err
	}})
	return b
}

func deviceCommand(o *options) *cobra.Command {
	d := &cobra.Command{Use: "device", Short: "Inspect Android device connectivity"}
	d.AddCommand(&cobra.Command{Use: "connect <host:port>", Short: "Connect an already-paired Android device over Wi-Fi", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := android.New(o.adbBinary, "").Connect(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ Connected to Android device at %s\n", args[0])
		return nil
	}})
	d.AddCommand(&cobra.Command{Use: "info", Short: "Show Android connection and storage", RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := config.Load(o.configPath)
		if err != nil {
			return err
		}
		device := android.New(o.adbBinary, c.Android.DeviceSerial)
		info, err := device.Info(cmd.Context())
		if err != nil {
			return err
		}
		storage, err := device.Storage(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Serial: %s\nModel: %s\nAndroid: %s\nDestination: %s\nAvailable: %.1f GB\n", info.Serial, info.Model, info.Android, c.Android.Destination, float64(storage.AvailableBytes)/(1024*1024*1024))
		return nil
	}})
	return d
}

func serviceCommand(o *options) *cobra.Command {
	s := &cobra.Command{Use: "service", Short: "Install the optional systemd user service"}
	s.AddCommand(&cobra.Command{Use: "install", Short: "Install, enable, and start the user service", RunE: func(cmd *cobra.Command, _ []string) error {
		if _, err := config.Load(o.configPath); err != nil {
			return err
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		path, err := service.InstallUser(cmd.Context(), binary, o.configPath, o.dbPath, o.adbBinary)
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), path)
		}
		return err
	}})
	return s
}

func openRuntime(ctx context.Context, o *options) (*runtime, error) {
	c, err := config.Load(o.configPath)
	if err != nil {
		return nil, err
	}
	key, err := config.ReadAPIKey(c.Immich.APIKeyFile)
	if err != nil {
		return nil, err
	}
	client, err := immich.New(c.Immich.URL, key)
	if err != nil {
		return nil, err
	}
	s, err := openStore(o.dbPath)
	if err != nil {
		return nil, err
	}
	device := android.New(o.adbBinary, c.Android.DeviceSerial)
	return &runtime{config: c, store: s, client: client, device: device, engine: &syncer.Engine{Config: c, Store: s, Immich: client, Device: device, CacheDir: config.CacheDir(), Log: loggerFor(o.logLevel)}}, nil
}

// Cleanup deliberately does not require a live Immich connection or the API
// key. Confirmation and removal concern only already-recorded Android paths.
func openCleanupRuntime(ctx context.Context, o *options) (*runtime, error) {
	c, err := config.Load(o.configPath)
	if err != nil {
		return nil, err
	}
	s, err := openStore(o.dbPath)
	if err != nil {
		return nil, err
	}
	device := android.New(o.adbBinary, c.Android.DeviceSerial)
	return &runtime{config: c, store: s, device: device, engine: &syncer.Engine{Config: c, Store: s, Device: device}}, nil
}

func openStore(path string) (*store.Store, error) { return store.Open(path) }
func loggerFor(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
func printReport(out io.Writer, r syncer.Report) {
	fmt.Fprintf(out, "discovered=%d queued=%d transferred=%d failed=%d skipped=%d", r.Discovered, r.Queued, r.Transferred, r.Failed, r.Skipped)
	if r.BatchID != "" {
		fmt.Fprintf(out, " batch=%s", r.BatchID)
	}
	if r.StorageLow {
		fmt.Fprint(out, " status=STORAGE_LOW")
	}
	fmt.Fprintln(out)
}
func ask(in *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	v, err := in.ReadString('\n')
	if err != nil && len(v) == 0 {
		return "", err
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultValue, nil
	}
	return v, nil
}
func askInt(in *bufio.Reader, out io.Writer, label string, def int64) (int64, error) {
	v, err := ask(in, out, label, strconv.FormatInt(def, 10))
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", label, err)
	}
	return n, nil
}
func askYesNo(in *bufio.Reader, out io.Writer, label string, def bool) (bool, error) {
	fallback := "y"
	if !def {
		fallback = "n"
	}
	v, err := ask(in, out, label+" [y/n]", fallback)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(v) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, errors.New("please answer y or n")
	}
}
func askSecret(raw io.Reader, in *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprintf(out, "%s: ", label)
	if f, ok := raw.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		return strings.TrimSpace(string(b)), err
	}
	v, err := in.ReadString('\n')
	return strings.TrimSpace(v), err
}
