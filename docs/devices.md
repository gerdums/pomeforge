# Test on a physical iPhone or iPad from Linux

Use a native Linux host for the first USB test. Complete the toolchain and SDK steps in the [CLI guide](cli.md), then connect your own iPhone or iPad with a data-capable USB cable. These instructions use xtool 1.19.0. Physical-device testing remains unverified in Pomeforge's [compatibility matrix](compatibility.md); successful compilation does not establish installation or launch support for your device and OS version. Pomeforge does not provide an iOS Simulator on Linux.

The examples assume an existing Pomeforge project at `./Garden`. Substitute your project's path. Run Pomeforge as your normal user, with the same `HOME` and XDG directories used during tool and SDK setup.

## 1. Install the Linux USB services

xtool uses `usbmuxd` to communicate with iOS over USB. Pomeforge's distro packages declare the USB service and utility dependencies, so the package manager installs them. For a portable or source installation, install the distro packages below. The runtime wizard does not run `sudo` or change device permissions.

| Distribution | Command |
| --- | --- |
| Omarchy / Arch | `sudo pacman -S --needed usbmuxd libimobiledevice` |
| Debian / Ubuntu | `sudo apt-get install usbmuxd libimobiledevice-utils` |
| Fedora | `sudo dnf install usbmuxd libimobiledevice-utils` |

Package references: [Arch usbmuxd](https://archlinux.org/packages/extra/x86_64/usbmuxd/), [Arch libimobiledevice](https://archlinux.org/packages/extra/x86_64/libimobiledevice/), [xtool's Debian/Ubuntu instructions](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/Installation-Linux.md), [Fedora usbmuxd](https://packages.fedoraproject.org/pkgs/usbmuxd/usbmuxd/), and [Fedora utilities](https://packages.fedoraproject.org/pkgs/libimobiledevice/libimobiledevice-utils/). On other distributions, use the equivalent packages and the distro's service instructions.

The packaged udev rules and service normally start `usbmuxd` when a device is plugged in. A stopped service with no device attached can be normal. On a systemd host, inspect a connection problem with:

```sh
usbmuxd --help
systemctl status usbmuxd.service
journalctl -u usbmuxd.service -b --no-pager
```

Some distributions start the daemon directly through udev rather than a persistent systemd service. Use the packaged rules and service account; do not fix access errors by running Pomeforge as root, making USB nodes world-writable, or starting a second daemon. The [usbmuxd usage guide](https://github.com/libimobiledevice/usbmuxd#usage) explains activation and the daemon's pairing-record directory.

## 2. Connect, trust, and select the device

Unlock the iPhone or iPad, connect it directly, and accept its **Trust This Computer** prompt. Enter the device passcode when asked. Trust is a device decision; a CLI flag cannot replace it. See [Apple's trust instructions](https://support.apple.com/en-us/109054).

First check USB enumeration, then ask Pomeforge to list devices:

```sh
idevice_id -l
pomeforge plan devices --project ./Garden
pomeforge run devices --project ./Garden --execute
```

Pomeforge runs `xtool devices --no-wait`; an empty list returns without waiting for a connection. The pinned command searches USB and network devices by default. Use the identifier from the USB list for the device physically connected in front of you. Keep identifiers out of committed configuration and public logs.

Store that identifier in a shell variable for the following commands:

```sh
printf 'Paste the device identifier: '
IFS= read -r pomeforge_device
```

If pairing is still required, the installed libimobiledevice utilities can request it explicitly:

```sh
idevicepair -u "$pomeforge_device" pair
idevicepair -u "$pomeforge_device" validate
```

Keep the device unlocked and accept any trust prompt, then retry a command that failed while you were responding. These utility options are defined in the upstream [device-list tool](https://github.com/libimobiledevice/libimobiledevice/blob/1.4.0/tools/idevice_id.c) and [pairing tool](https://github.com/libimobiledevice/libimobiledevice/blob/1.4.0/tools/idevicepair.c).

## 3. Configure development signing

You can compile before signing in to Apple:

```sh
pomeforge run build --project ./Garden --execute
```

Installing a development app requires an Apple identity and suitable development provisioning. Configure the pinned xtool interactively in a normal terminal. For a Pomeforge-managed installation:

```sh
case ${XDG_DATA_HOME:-} in
  /*) pomeforge_data_home=$XDG_DATA_HOME ;;
  *) pomeforge_data_home=$HOME/.local/share ;;
esac
pomeforge_xtool="$pomeforge_data_home/pomeforge/bin/xtool"
"$pomeforge_xtool" --version
"$pomeforge_xtool" auth login --mode key
```

The key mode uses Apple's public API and requires paid Apple Developer Program membership. Follow xtool's prompts and its documented Team API key requirements. If you use xtool's free-account path, run `auth login --mode password` instead; it uses private Apple APIs and is subject to Apple's account and provisioning restrictions. Enter credentials through the interactive prompts. Do not put a password in command arguments, Pomeforge plans, project files, or shared logs. The [pinned authentication source](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/AuthCommand.swift) defines these modes; the [Linux setup guide](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/Installation-Linux.md) describes the account prerequisites.

xtool can register the selected device, create an App ID, obtain a development certificate and profile, and change the app's signing identity or bundle ID. Its installer can also ask to revoke an existing certificate. Review that decision in an interactive terminal; Pomeforge's `--confirm` does not answer an upstream certificate-revocation prompt. See the [installation flow](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/First-app.tutorial) and [revocation prompt implementation](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/XToolInstallerDelegate.swift).

## 4. Install, enable Developer Mode, and launch

Preview the device and signing effects before executing:

```sh
pomeforge plan install --project ./Garden --device "$pomeforge_device"
pomeforge run install --project ./Garden --device "$pomeforge_device" --execute --confirm
```

Without `--ipa`, this runs `xtool dev run --configuration debug --udid ...`, which builds, provisions, signs, and installs the app. `--execute` requests execution; `--confirm` additionally authorizes the installation and signing effects. The [pinned implementation](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/DevCommand.swift) installs the app but does not automatically launch it.

If the device requests Developer Mode, open **Settings > Privacy & Security > Developer Mode**, enable it, restart, and confirm after restart. The setting may appear only after pairing or a development-install attempt. Follow [Apple's Developer Mode instructions](https://developer.apple.com/documentation/xcode/enabling-developer-mode-on-a-device), then repeat the install command if necessary. No Mac step is part of this workflow.

Tap the installed app's icon. If iOS reports an untrusted developer, inspect the identity under **Settings > General > VPN & Device Management** and trust your own development identity when appropriate. xtool's [first-app tutorial](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/First-app.tutorial) documents this step.

For CLI launch:

```sh
pomeforge plan launch --project ./Garden --device "$pomeforge_device"
pomeforge run launch --project ./Garden --device "$pomeforge_device" --execute
```

Launch uses the bundle identifier in `pomeforge.json`. If provisioning changed the installed identifier, use the actual installed app's icon and resolve the identifier mismatch before retrying. xtool's [launch command](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/LaunchCommand.swift) uses the device's debugserver service. Successful USB enumeration or installation alone does not prove that this service works on a particular iOS version.

## Installing an existing IPA

Keep the IPA inside the selected Pomeforge workspace, review its exact path, and use:

```sh
pomeforge plan install --project ./Garden --device "$pomeforge_device" --ipa ./Garden.ipa
pomeforge run install --project ./Garden --device "$pomeforge_device" --ipa ./Garden.ipa --execute --confirm
```

This invokes xtool's provisioning/signing installer. It may re-sign the app and change its identity; it does not promise to preserve an existing distribution signature. Keep your exported release IPA separately and do not treat a successful device install as proof that those release bytes were tested. Pomeforge currently has no adapter for installing an IPA while guaranteeing preservation of its existing signature. See the [pinned install source](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/InstallCommand.swift) and [distribution guide](distribution.md).

## Connection problems and containers

If `idevice_id -l` is empty, check the cable, unlocked device, host USB access, udev rules, and daemon logs before investigating signing. If it lists a device but pairing fails, respond to the device's trust prompt and validate pairing again. If installation succeeds but launch fails, check Developer Mode, developer trust, the installed bundle ID, and support for that OS version's developer services. Do not erase pairing records as a routine first step.

For container use, read [physical USB devices](container.md#physical-usb-devices). A container does not gain USB access merely because it can compile an iOS app. Host-socket access, permissions, pairing, and developer services need separate testing. When using the host usbmuxd socket, the host daemon manages pairing records, normally under `/var/lib/lockdown`; Pomeforge's XDG state does not relocate them. A socket mount is not proof of a working device connection, and Pomeforge has not verified this container device path. Start with native Linux USB access before adding that extra layer.

To record a completed device test, retain the Pomeforge result, the source revision, the actual installed app identity, the device OS version, and a screenshot or recording of the tested flow. Keep private device identifiers out of public evidence. Device proof and App Store processing remain separate entries in the [verification record](verification.md).
