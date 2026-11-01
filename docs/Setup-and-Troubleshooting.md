# Setup and troubleshooting

This guide covers the practical setup issues most likely to block a first
Immich Relay run. Relay reads original files from Immich and writes them to
Android over ADB; Google Photos backup is configured separately on the phone.

## Install or upgrade

Install the latest published Linux release with:

```bash
curl -fsSL https://raw.githubusercontent.com/ifeelsam/immich-relay/main/scripts/install.sh | sh
```

The installer puts the binary in `~/.local/bin`. Add that directory to your
shell `PATH` if necessary, then confirm the installed version:

```bash
export PATH="$HOME/.local/bin:$PATH"
immich-relay version
```

To install a specific version, it must have a matching GitHub Release and
release assets:

```bash
curl -fsSL https://raw.githubusercontent.com/ifeelsam/immich-relay/main/scripts/install.sh | sh -s -- 0.1.4
```

### Installer returns HTTP 404

Use the `ifeelsam/immich-relay` installer URL shown above. A 404 normally
means the repository name is wrong or that the requested version has not been
published with its Linux archives and `checksums.txt` yet. See the project's
[Releases](https://github.com/ifeelsam/immich-relay/releases) page for the
available version numbers.

## Immich server address

The wizard accepts both public origins and local servers, including:

```text
http://localhost:2283
http://192.168.1.50:2283
https://photos.example.com
```

`localhost` means the computer running Relay. If Immich is in a container or
on another machine, use an address that this computer can actually reach.

## Android destination

Both of these paths point at Android shared internal storage and are accepted:

```text
/sdcard/DCIM/ImmichRelay
/storage/emulated/0/DCIM/ImmichRelay
```

They are aliases, not a removable SD card. Relay rejects paths outside shared
storage and path traversal, because cleanup is restricted to tracked Relay
files below this destination.

## Connect Android with USB

Enable **Developer options → USB debugging**, use a data-capable cable, unlock
the phone, and accept its RSA authorization prompt. A healthy connection looks
like this:

```text
$ adb devices -l
<serial>  device  ...
```

### `no permissions; see ...`

For example:

```text
a1862ff873f6  no permissions; see [http://developer.android.com/tools/device.html]
```

This is a Linux USB/udev access problem. It occurs before Android can show an
authorization prompt, so unlocking the phone alone cannot fix it. On Arch:

```bash
sudo pacman -S android-udev
getent group adbusers >/dev/null && sudo usermod -aG adbusers "$USER"
```

Log out and back in if the group was added, unplug and reconnect the phone,
then restart ADB and check again:

```bash
adb kill-server
adb devices -l
```

If the result is still `no permissions`, check the phone's vendor and product
ID with `lsusb`; it may not be covered by the installed rules. Add a matching
udev rule as described in the [ArchWiki ADB guide](https://wiki.archlinux.org/title/Android_Debug_Bridge).
Do not run the Relay daemon with `sudo`; doing so creates a separate root ADB
key and makes the user service harder to manage.

### `unauthorized`

The host can see the phone but has not been approved. Unlock the phone and
accept the USB debugging fingerprint. If no prompt appears, use **Developer
options → Revoke USB debugging authorizations**, reconnect, and accept the new
prompt.

## Connect Android with Wireless debugging

Wireless debugging is ADB over Wi-Fi. It avoids USB udev permissions and is
supported on Android 11 and later. The computer and phone must be on the same
Wi-Fi network.

1. On the phone, enable **Developer options → Wireless debugging**.
2. Choose **Pair device with pairing code**. It shows an IP address, pairing
   port, and pairing code.
3. On the computer, pair with that pairing port:

   ```bash
   adb pair <phone-ip>:<pairing-port>
   ```

   Enter the code when prompted.

4. The Wireless debugging screen also shows a separate connection/debug port.
   Connect Relay to that port:

   ```bash
   immich-relay device connect <phone-ip>:<debug-port>
   adb devices -l
   ```

5. The device list must show `<phone-ip>:<debug-port>  device`. Then run:

   ```bash
   immich-relay setup
   ```

With `android.device_serial: auto`, Relay selects exactly one authorized ADB
device. It ignores USB entries in `no permissions`, `unauthorized`, or
`offline` states. If two authorized devices are connected, enter the desired
`<phone-ip>:<debug-port>` value at the wizard's Android serial prompt.

Wireless debugging's port can change when the phone changes networks, toggles
Wireless debugging, or is locked. Re-run the `device connect` command with the
current debug port; pair again if Android asks for it.

## Final readiness check

Before the first transfer:

```bash
immich-relay doctor
immich-relay sync --once --dry-run
```

In Google Photos, enable backup for the `ImmichRelay` device folder. Relay can
verify delivery to Android, but it cannot prove that Google Photos has uploaded
an individual file; only confirm a Relay batch after checking Google Photos.
