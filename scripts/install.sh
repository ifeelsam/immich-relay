#!/bin/sh
# Install a published Immich Relay Linux release for the current architecture.
# Usage: curl -fsSL https://raw.githubusercontent.com/ifeelsam/immich-relay/main/scripts/install.sh | sh -s -- 0.1.3
set -eu

version="${1:-${IMMICH_RELAY_VERSION:-0.1.3}}"
repo="${IMMICH_RELAY_REPO:-ifeelsam/immich-relay}"
prefix="${IMMICH_RELAY_PREFIX:-$HOME/.local}"

case "$(uname -s)" in
  Linux) ;;
  *) echo "Immich Relay releases are currently packaged for Linux only." >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="x86_64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "Unsupported CPU architecture: $(uname -m)" >&2; exit 1 ;;
esac

archive="immich-relay_${version}_Linux_${arch}.tar.gz"
directory="immich-relay_${version}_Linux_${arch}"
base_url="https://github.com/${repo}/releases/download/v${version}"
workdir="$(mktemp -d "${TMPDIR:-/tmp}/immich-relay.XXXXXX")"
trap 'rm -rf "$workdir"' EXIT HUP INT TERM

echo "Downloading Immich Relay ${version} for Linux ${arch}..."
curl -fsSL --retry 3 -o "$workdir/$archive" "$base_url/$archive"
curl -fsSL --retry 3 -o "$workdir/checksums.txt" "$base_url/checksums.txt"

expected="$(awk -v file="$archive" '$2 == file { print $1 }' "$workdir/checksums.txt")"
if [ -z "$expected" ]; then
  echo "No checksum for $archive in the release manifest." >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$workdir/$archive" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$workdir/$archive" | awk '{print $1}')"
else
  echo "A SHA-256 utility (sha256sum or shasum) is required." >&2
  exit 1
fi
if [ "$expected" != "$actual" ]; then
  echo "Checksum verification failed; installation aborted." >&2
  exit 1
fi

tar -xzf "$workdir/$archive" -C "$workdir"
install -d "$prefix/bin"
install -m 0755 "$workdir/$directory/immich-relay_${version}_Linux_${arch}" "$prefix/bin/immich-relay"
echo "Installed $prefix/bin/immich-relay"
echo "Add $prefix/bin to PATH if needed, then run: immich-relay setup"
