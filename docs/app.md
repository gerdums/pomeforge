# Pomeforge graphical workspace

Pomeforge's graphical workspace is a local browser application served by the `pomeforge` binary. It uses the same plans and executor as the CLI; it is not an iOS simulator, an editor, or a substitute for a real-device and App Store verification run.

## Start the workspace

Run Pomeforge against a directory that will contain your projects:

```sh
pomeforge app --open --workspace "$HOME/Pomeforge" --listen 127.0.0.1:0
```

The ephemeral loopback port avoids collisions. Pomeforge binds first, creates a short-lived authenticated session, prints its token URL, and asks Linux to open that URL with `xdg-open`. The browser removes the token fragment immediately, retains the token only in memory, and sends it as a bearer token for API requests. Do not copy the startup URL into logs or issue reports.

If the page reports a missing or expired session, close the tab and start `pomeforge app --open` again. If the local process briefly becomes unavailable, use **Retry connection** in the error notice.

## What the workspace does

- Selects or creates an xtool Swift package inside the configured workspace.
- Opens **Prerequisites** even when the workspace has no projects. From there,
  a person can preview and explicitly run a pinned tool install,
  integrity-register an existing `pomeforge-assets` or `unxip` helper, inspect
  SDK status, or import an operator-supplied Xcode.app/XIP. Setup input changes
  invalidate the preview.
- Displays the tool and capability diagnostics reported by Pomeforge, including missing, incompatible, limited, and unverified states.
- Requests an operation plan and shows its executable, each exact argument boundary, working directory, blockers, and warnings.
- Runs only the returned plan identifier. Operations marked as externally mutating require an explicit confirmation checkbox.
- Displays actual process status, exit code, timestamps, and output, including actual output returned with a failed run. Setup completion remains visibly announced after its one-use plan is cleared and diagnostics refresh. Results identify project scope or workspace scope so a global setup result cannot be mistaken for a selected app. Refreshing diagnostics merges backend history with results from the current browser session by result ID, so a temporarily empty history response does not discard a completed run. An empty result log means that no retained runs were reported.

Changing a project, action, IPA path, or device identifier invalidates the current plan. Tool installation links are shown only when Pomeforge reports a valid HTTPS URL. The application has no remote scripts, fonts, analytics, or frontend-only fixture mode.

## Release workflow

The graphical workspace exposes the same release operations as the CLI. In
**Prerequisites**, use **Configure signing identity** or **Inspect signing
identity**; private paths are sent only to the authenticated loopback API and
are never retained in public results. In **Work**, select `release-build`,
`icons`, `export`, `ipa-inspect`, `upload`, `validate`, `store-status`, or
`submit`, fill the fields shown for that action, review the generated plan, and
then run its one-use identifier. Upload and submit show a separate confirmation
gate, and upload never triggers submission.

Every form mutation and project switch invalidates the displayed plan. Release
paths are project-relative unless the UI explicitly requires an absolute
private signing path. Results distinguish workspace-scoped identity operations
from project-scoped build/export/store operations and render returned text as
text, not HTML. Local success does not claim Apple processing or acceptance.

Stable browser-flow selectors for the parent runtime proof are
`#project-select`, `#action-select`, `#source-bundle-input`,
`#icon-source-input`, `#output-ipa-input`, `#asset-catalog-input`,
`#identity-select`, `#app-id-input`, `#version-id-input`, `#build-id-input`,
`#review-plan`, `#plan-panel`, `#warning-list`, `#confirm-execution`,
`#run-plan`, and `#result-list`. Signing setup uses `#setup-action-select`,
`#setup-identity-id`, `#setup-identity-label`, `#setup-private-key`,
`#setup-certificate`, `#setup-profile`, `#setup-trust-roots`,
`#review-setup-plan`, and `#run-setup-plan`.

## Linux desktop launcher

The user-local installer accepts an already-built Pomeforge binary. It never downloads toolchains, runs a package manager, or requests root access.

```sh
./packaging/install.sh ./pomeforge
```

It writes only these Pomeforge-owned files beneath `${XDG_DATA_HOME:-$HOME/.local/share}`:

- `pomeforge/bin/pomeforge`
- `pomeforge/bin/pomeforge-workspace`
- `pomeforge/workspace/` (created if absent; existing contents are preserved)
- `applications/pomeforge.desktop`
- `icons/hicolor/scalable/apps/pomeforge.svg`

The launcher uses `${POMEFORGE_WORKSPACE}` when set, otherwise an absolute `${XDG_DATA_HOME}/pomeforge/workspace` or `$HOME/.local/share/pomeforge/workspace`. A relative `XDG_DATA_HOME` is ignored consistently by the installer and launcher in favor of that standard home-directory default. Workspace paths containing spaces or percent characters are passed as single arguments. The installer rejects an install root containing a percent sign, an equals sign, control characters, or non-ASCII characters before writing files because those executable paths cannot be represented reliably in a desktop entry. It starts the documented command directly, without a shell evaluation step:

```text
pomeforge app --open --workspace PATH --listen 127.0.0.1:0
```

The core process owns browser opening after a successful bind. The launcher does not parse or redirect its token-bearing output. This browser-based desktop flow works on distributions with an XDG-compatible browser opener, including Omarchy; no WebKit runtime is required.

## Prerequisites and limits

The graphical workspace requires a Linux desktop browser and `xdg-open` for automatic opening. Pomeforge itself reports the status of xtool, Swift and its iOS SDK, ASC CLI, and USB/device support. The desktop binary installer deliberately does not install those upstream tools; use the graphical Prerequisites actions for supported managed installs and helper registration. Apple input and native prerequisites are described in [Linux setup and SDK import](setup.md).

Apple Developer Program membership, credentials, signing authorization, compatible provisioning, device trust and Developer Mode, and App Store eligibility remain external prerequisites. A successful local plan is not evidence of a successful iOS build, device launch, TestFlight upload, or App Review submission.

## Uninstall

Close any running Pomeforge process, then remove only the installed files:

```sh
case ${XDG_DATA_HOME:-} in
  /*) data_home=$XDG_DATA_HOME ;;
  *) data_home=$HOME/.local/share ;;
esac
rm -f -- "$data_home/applications/pomeforge.desktop"
rm -f -- "$data_home/icons/hicolor/scalable/apps/pomeforge.svg"
rm -f -- "$data_home/pomeforge/bin/pomeforge-workspace"
rm -f -- "$data_home/pomeforge/bin/pomeforge"
```

The workspace directory and its projects are intentionally preserved. Remove `$data_home/pomeforge/workspace` separately only if you have reviewed and no longer need its contents.
