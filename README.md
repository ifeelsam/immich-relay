# Immich Relay

Immich Relay is a small Linux daemon that copies **original** media from an [Immich](https://immich.app/) library to an Android phone attached by USB. Google Photos on that phone can then back up the copied media using its normal Android backup flow.

It is deliberately a relay, not a second cloud uploader. Immich remains the source of truth. Relay never deletes or modifies Immich assets, and it never claims a file is in Google Photos just because it reached the phone.

```text
Camera / phone / imports
          │
          ▼
      Immich server ── original bytes ──► Immich Relay ── ADB/USB ──► Android
                                                                          │
                                                              Google Photos backup
```

## Safety model

- Downloads use Immich's public REST API; Relay never reads Immich's database or storage.
- The SQLite queue records each asset ID and survives restarts, USB loss, and host reboots. Interrupted transfers return to the queue on startup.
- Android target names include the immutable Immich asset ID, avoiding filename collisions with unrelated phone files.
- A remote SHA-256 is compared where Android provides `sha256sum`. If it does not, Relay explicitly falls back to file-size verification and logs that weaker guarantee.
- A MediaStore scan is requested after each verified push, so media is visible to applications including Google Photos on supported Android versions.
- `TRANSFERRED` / `AWAITING_BACKUP_CONFIRMATION` is **not** cloud confirmation. Only `immich-relay batch confirm` permits `batch cleanup`, and cleanup is constrained to tracked files below Relay's destination directory.

No non-root Android mechanism can prove that Google Photos has uploaded every individual file. Do not configure automatic cleanup. Smart Storage may remove files independently; that is not treated as upload confirmation and Relay does not retransmit a previously handled asset unless you explicitly requeue it.

## Requirements

- A 64-bit Linux machine (`x86_64` or `aarch64`)
- An Immich API key with permission to read your library
- Android Platform Tools (`adb`), USB debugging enabled, and an authorized USB connection
- Google Photos configured on the phone to back up the `ImmichRelay` device folder

The binary is standalone. ADB is intentionally the only external runtime dependency.

## Install

Download the matching archive from GitHub Releases, verify its published checksum, then install it:

```bash
tar -xzf immich-relay_*_Linux_x86_64.tar.gz
install -Dm755 immich-relay ~/.local/bin/immich-relay
```

On Arch Linux (and other Linux distributions), the same static `x86_64` or
`aarch64` archive works directly. Once a release is published, a verified
user-local curl installer is also available:

```bash
curl -fsSL https://raw.githubusercontent.com/immich-relay/immich-relay/main/scripts/install.sh | sh -s -- 0.1.0
```

It installs to `~/.local/bin`; change that with `IMMICH_RELAY_PREFIX`. The
installer verifies the selected archive against that release's `checksums.txt`
before installing it.

For a development build, Go 1.26 or newer is required:

```bash
git clone https://github.com/immich-relay/immich-relay.git
cd immich-relay
go build -o immich-relay ./cmd/immich-relay
```

### Arch Linux / AUR

The repository contains two package definitions under `packaging/aur/`:

- `immich-relay`: builds the tagged source with Go.
- `immich-relay-bin`: installs a release archive.

They are local packaging definitions, not published AUR packages. Before an AUR submission, replace the `SKIP` checksum in `immich-relay-bin/PKGBUILD` with the release checksum and regenerate `.SRCINFO`.

For a local source-package check after releasing a tag:

```bash
cd packaging/aur/immich-relay
makepkg -si
```

Debian (`.deb`) and RPM packages are built by GoReleaser when a release is made. They declare `adb` / `android-tools` as an external dependency.

## First-time setup

1. On Android, enable **Developer options → USB debugging**. Connect the phone, accept its RSA authorization dialog, and check it with `adb devices -l`.
2. In Google Photos, turn on backup and enable its **Back up device folders** setting for `ImmichRelay`. Google may change this UI; verify with a small test image before a large transfer.
3. Run the wizard:

   ```bash
   immich-relay setup
   ```

The wizard validates the Immich API key, reports the device model and Android version, checks available storage, writes `~/.config/immich-relay/config.yaml`, stores the API key in a mode-`0600` file, initializes SQLite state, and can enable a systemd user service.

The default first-run mode is `new-only`: Relay performs an explicit metadata baseline and ignores existing library items. Choose `full` if you want the whole current library. `after-date` accepts an RFC3339 timestamp.

Example configuration:

```yaml
immich:
  url: "https://photos.example.com"
  api_key_file: "~/.config/immich-relay/api-key"
android:
  device_serial: "auto"
  destination: "/sdcard/DCIM/ImmichRelay"
sync:
  interval: "5m"
  initial_mode: "new-only"
  transfer_concurrency: 1
  retry_attempts: 5
  reconciliation_interval: "24h"
storage:
  min_free_space_gb: 20
  max_batch_size_gb: 15
cleanup:
  strategy: "manual-confirmation"
  use_smart_storage: false
```

The destination must be below `/sdcard/`. Single-transfer concurrency is intentional: it makes the storage reserve and no-clobber behavior predictable.

## Everyday use

```bash
# Verify both endpoints and local prerequisites
immich-relay doctor

# Preview discovery without moving anything
immich-relay sync --once --dry-run

# Run one complete transfer pass
immich-relay sync --once

# Inspect outstanding work
immich-relay status
immich-relay queue list

# Retry errors immediately rather than waiting for exponential backoff
immich-relay queue retry

# Explicitly retransmit a previously handled item, for example after Smart Storage
immich-relay queue requeue <immich-asset-id>

# View the batches that await a real Google Photos check
immich-relay batch list

# Only after checking the corresponding files in Google Photos:
immich-relay batch confirm 20261101T120000Z-...
immich-relay batch cleanup 20261101T120000Z-...
```

`run` is the continuous daemon command. It makes a pass at startup, then uses the configured polling interval. Incremental discovery uses the last observed timestamp; a periodic full reconciliation catches items missed while Relay was offline. Temporary API, ADB, and download failures use exponential retry.

## systemd

`immich-relay setup` can install a user service, or do it later with:

```bash
immich-relay service install
systemctl --user status immich-relay
journalctl --user -u immich-relay -f
```

For a user service to continue after logout, enable lingering once:

```bash
loginctl enable-linger "$USER"
```

`packaging/systemd/immich-relay-system.service` is an optional template for a dedicated server account. Review its paths and USB/udev permissions before enabling it; running the user service is preferred.

## Troubleshooting

- **`no Android devices found`**: use a data-capable cable, enable USB debugging, then run `adb kill-server && adb devices -l` and authorize the host on the unlocked phone.
- **`unauthorized`**: revoke USB debugging authorizations on Android, reconnect, and accept the new fingerprint prompt.
- **ADB permissions on Linux**: install the distribution's Android platform-tools package. Some distributions also require a udev rule or membership in `plugdev`; follow their current package documentation, then reconnect.
- **`STORAGE_LOW`**: Relay pauses new transfers without losing the queue. Free space, wait for Google Photos/Smart Storage as appropriate, and run again.
- **Google Photos does not show a file**: wait briefly and check the folder's backup setting. Relay invokes Android's public media scanner broadcast, but OEM restrictions or Android changes may prevent immediate indexing. Relay reports the transfer as awaiting confirmation, never as backed up.
- **Unsupported files**: use `immich-relay queue list --state FAILED`; formats are retained as visible failures rather than silently discarded.

## Development and releases

```bash
go test ./...
go vet ./...
goreleaser check
goreleaser release --snapshot --clean
```

The test suite uses mocked Immich and Android boundaries for pagination, restarts, low storage, download interruption, checksums, collision-safe paths, confirmation, and cleanup. Hardware validation is intentionally manual: use a spare Android device and `sync --once --dry-run` first.

The project is MIT licensed. Contributions should preserve the core rule: do not mutate Immich originals and do not represent local Android presence as Google Photos backup confirmation.
