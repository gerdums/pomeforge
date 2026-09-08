# Orchard CLI

Orchard is a local Linux CLI and authenticated loopback workspace for SwiftUI
iPhone and iPad projects. It uses JSON envelopes for automation and readable
text by default. Run `orchard schema --json` to discover commands, parameters,
effects, and every supported action.

## Build and start

Go 1.24 or newer is required to build Orchard itself.

```sh
make check
./bin/orchard version
```

Start the browser workspace on an automatically selected loopback port:

```sh
./bin/orchard app --workspace /path/to/workspace --listen 127.0.0.1:0
```

Orchard prints one usable URL such as
`Orchard app: http://127.0.0.1:43127/#token=...`. The fragment is delivered to
the local app and is never sent in the initial HTTP request. Add `--open` to
start `xdg-open` directly with an argv array. If no opener is installed, the
server stays available and prints a warning. Foreign bind addresses are
rejected. With global `--json`, the startup record is a success envelope whose
data contains the same `url` instead of the human line.

## Global JSON output

`--json` is accepted before or after every command. Successes use
`{"ok":true,"data":...}` and errors use
`{"ok":false,"error":{"code":"...","message":"..."}}`.

```sh
orchard --json doctor
orchard doctor --json
orchard schema --json
```

Unknown flags and arguments are rejected. Usage and validation errors exit 2,
blocked or unconfirmed operations exit 3, conflicts such as stale plans exit 4,
and an executed child process returns its real nonzero exit status.

## Create a project

```sh
orchard init Garden --dir ./Garden --bundle-id com.example.Garden
```

`--dir` is the new project's destination, not an existing directory to reuse.
Orchard refuses to overwrite any existing path, including a directory containing
untracked files. Names start with a letter and contain letters, digits, or
underscores. Bundle identifiers contain at least two dot-separated alphanumeric
or hyphenated components.

The generated project includes:

- `orchard.json`, schema version 1, with no credentials;
- a Swift package library product and SwiftUI `@main` app target;
- `xtool.yml` version 1 with the selected bundle ID;
- `Info.plist` with iPhone and iPad device families and orientations;
- `.sourcekit-lsp/config.json`, project ignores, and release-boundary guidance.

## Diagnose prerequisites

```sh
orchard doctor
orchard tools --json
```

These commands execute bounded version probes for `xtool`, `swift`, `asc`,
`zsign`, `idevice_id`, and `usbmuxd`. Missing, incompatible, and unverified tools
remain explicit and include upstream installation URLs. Orchard never pipes an
installer into a shell, invokes `sudo`, or downloads Apple's SDK. SDK setup needs
a user-obtained Xcode archive and the explicit upstream command
`xtool sdk install /path/to/Xcode.xip`.

## Inspect and run actions

Every operation is planned first. A plan ID covers validated inputs, project
manifest/source/config content, referenced IPA bytes, and relevant tool state.

```sh
orchard plan build --project ./Garden
orchard run build --project ./Garden --execute

orchard plan install --project ./Garden --device DEVICE_UDID --ipa ./Garden.ipa
orchard run install --project ./Garden --device DEVICE_UDID --ipa ./Garden.ipa --execute
```

`run` always requires `--execute`. App Store account writes also require
`--confirm`. Browser callers submit only action/project/input data to `/api/plan`
and later submit a server-held `planId` to `/api/run`; they cannot supply an
executable or argv. Changed sources, manifests, tool state, or IPA content make a
stored plan stale and require a new inspection.

| Action | Effect | Command boundary |
| --- | --- | --- |
| `setup` | local read | `swift --version`; `xtool sdk status`; manual Xcode archive guidance |
| `build` | local build | `xtool dev build --configuration debug` |
| `devices` | device read | `xtool devices --no-wait` |
| `install` | device write | `xtool install --udid DEVICE PATH`, or `xtool dev run --configuration debug --udid DEVICE` without `--ipa` |
| `launch` | device write | `xtool launch --udid DEVICE BUNDLE_ID` |
| `export` | local build | `xtool dev build --configuration release --ipa` (unsigned) |
| `store-status` | account read | `asc builds info --build-id BUILD_ID --output json` |
| `validate` | account read | `asc validate --app APP_ID` with exactly one configured version selector |
| `upload` | account write | `asc builds upload --app APP_ID --ipa PATH --wait --output json` |
| `submit` | account write | `asc review submit ... --dry-run`, followed only after confirmation by `asc review submit ... --confirm` |

`asc submit create` is not supported because ASC 5 removed it.

The generated export is an unsigned development-workflow artifact. Orchard does
not yet perform or validate App Store distribution signing. Upload therefore
accepts only a pre-exported IPA that the user has independently verified as
correctly distribution-signed, and warns about that unverified prerequisite.
Development provisioning is never described as App Store distribution signing.

## Manifest App Store IDs

Read-only validation and account writes use resource IDs from `orchard.json`:

```json
{
  "appStore": {
    "appId": "123456789",
    "versionId": "987654321",
    "buildId": "456789123"
  }
}
```

These are identifiers, not credentials. ASC authentication remains in its
private user configuration. Orchard passes a narrow child environment including
`HOME`, `PATH`, XDG locations, and ASC settings, disables ASC telemetry for
managed subprocesses, caps and redacts retained output, and stores operation
history in private `.orchard/history.jsonl` files.

## Verification boundary

The Go tests prove Orchard's local validation, planning, process control,
history, CLI envelopes, and loopback HTTP protections. They do not prove an iOS
SDK installation, Apple account access, distribution signing, a physical-device
run, upload processing, or App Review submission. Those require separate trusted
host, hardware, and account evidence.
