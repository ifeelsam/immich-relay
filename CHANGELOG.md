# Changelog

All notable changes to Immich Relay are documented here.

## [0.1.2] - 2026-11-01

### Fixed

- Accept valid local Immich origins such as `http://localhost:2283` and LAN IP
  addresses with a port during setup and configuration validation.

## [0.1.1] - 2026-11-01

### Fixed

- Accept Android's canonical shared internal-storage path,
  `/storage/emulated/0/...`, alongside the `/sdcard/...` alias.
- Reject path traversal while validating either Android storage path.
- Correct GitHub repository URLs used by the installer, documentation,
  GoReleaser metadata, and Arch package definitions.

## [0.1.0] - 2026-11-01

### Added

- Durable SQLite-backed Immich-to-Android relay with original-file downloads,
  ADB delivery, integrity verification, MediaStore scanning, and safe retries.
- Explicit Google Photos confirmation batches and guarded Android-side cleanup.
- CLI setup, diagnostics, daemon support, systemd units, GoReleaser metadata,
  Arch package definitions, and checksum-verified Linux release archives.
