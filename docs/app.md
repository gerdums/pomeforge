# Orchard graphical workspace

Orchard's graphical workspace is a local browser application served by the `orchard` binary. It uses the same plans and executor as the CLI; it is not an iOS simulator, an editor, or a substitute for a real-device and App Store verification run.

## Start the workspace

Run Orchard against a directory that will contain your projects:

```sh
orchard app --open --workspace "$HOME/Orchard" --listen 127.0.0.1:0
```

The ephemeral loopback port avoids collisions. Orchard binds first, creates a short-lived authenticated session, prints its token URL, and asks Linux to open that URL with `xdg-open`. The browser removes the token fragment immediately, retains the token only in memory, and sends it as a bearer token for API requests. Do not copy the startup URL into logs or issue reports.

If the page reports a missing or expired session, close the tab and start `orchard app --open` again. If the local process briefly becomes unavailable, use **Retry connection** in the error notice.

## What the workspace does

- Selects or creates an xtool Swift package inside the configured workspace.
- Displays the tool and capability diagnostics reported by Orchard, including missing, incompatible, limited, and unverified states.
- Requests an operation plan and shows its executable, each exact argument boundary, working directory, blockers, and warnings.
- Runs only the returned plan identifier. Operations marked as externally mutating require an explicit confirmation checkbox.
- Displays actual process status, exit code, timestamps, and output. An empty result log means that no retained runs were reported.

Changing a project, action, IPA path, or device identifier invalidates the current plan. Tool installation links are shown only when Orchard reports a valid HTTPS URL. The application has no remote scripts, fonts, analytics, or frontend-only fixture mode.

## Linux desktop launcher

The user-local installer accepts an already-built Orchard binary. It never downloads toolchains, runs a package manager, or requests root access.

```sh
./packaging/install.sh ./orchard
```

It writes only these Orchard-owned files beneath `${XDG_DATA_HOME:-$HOME/.local/share}`:

- `orchard/bin/orchard`
- `orchard/bin/orchard-workspace`
- `orchard/workspace/` (created if absent; existing contents are preserved)
- `applications/orchard.desktop`
- `icons/hicolor/scalable/apps/orchard.svg`

The launcher uses `${ORCHARD_WORKSPACE}` when set, otherwise `${XDG_DATA_HOME:-$HOME/.local/share}/orchard/workspace`. Paths containing spaces are passed as single arguments. It starts the documented command directly, without a shell evaluation step:

```text
orchard app --open --workspace PATH --listen 127.0.0.1:0
```

The core process owns browser opening after a successful bind. The launcher does not parse or redirect its token-bearing output. This browser-based desktop flow works on distributions with an XDG-compatible browser opener, including Omarchy; no WebKit runtime is required.

## Prerequisites and limits

The graphical workspace requires a Linux desktop browser and `xdg-open` for automatic opening. Orchard itself reports the status of xtool, Swift and its iOS SDK, ASC CLI, and USB/device support. Follow the HTTPS installation guidance displayed by diagnostics; the desktop installer deliberately does not install those upstream tools.

Apple Developer Program membership, credentials, signing authorization, compatible provisioning, device trust and Developer Mode, and App Store eligibility remain external prerequisites. A successful local plan is not evidence of a successful iOS build, device launch, TestFlight upload, or App Review submission.

## Uninstall

Close any running Orchard process, then remove only the installed files:

```sh
data_home=${XDG_DATA_HOME:-"$HOME/.local/share"}
rm -f -- "$data_home/applications/orchard.desktop"
rm -f -- "$data_home/icons/hicolor/scalable/apps/orchard.svg"
rm -f -- "$data_home/orchard/bin/orchard-workspace"
rm -f -- "$data_home/orchard/bin/orchard"
```

The workspace directory and its projects are intentionally preserved. Remove `${XDG_DATA_HOME:-$HOME/.local/share}/orchard/workspace` separately only if you have reviewed and no longer need its contents.
