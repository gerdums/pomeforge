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
./bin/orchard --help
```

The provided `make test`, `make vet`, `make build`, and `make cross-check`
entrypoints require a Linux host, reject a non-Linux `HOSTS` value, and set
`GOOS=linux`. Compile, resource, signing, export, and install workflows are also
blocked by Orchard itself when its runtime is not Linux.

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
`{"ok":false,"error":{"code":"...","message":"..."}}`. If an operation
completed but its private receipt could not be stored, the error additionally
contains the bounded, redacted operation as `error.result`.

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

These commands execute bounded version probes and verify managed tool receipts.
Missing, incompatible, and unverified tools remain explicit and include upstream
installation URLs. Orchard never pipes an installer into a shell, invokes
`sudo`, or downloads Apple's SDK.

## Install tools and import an SDK

Inspect each pinned download before requesting installation:

```sh
orchard tools install xtool --json
orchard tools install xtool --execute
orchard tools install asc --execute
orchard tools install zsign --execute
```

Installation verifies the selected archive's size and SHA-256 and stores the
tool in private XDG state. It does not change the shell's global `PATH`.
Subsequent Orchard operations discover verified managed tools directly.

Swift 6.3 or newer and the separately built unxip helper are needed for XIP
import. The [Linux container](container.md) includes Swift, unxip and the
AssetKit bridge, and documents registering the bundled helpers. The
[native setup guide](setup.md) covers installation without a container.

```sh
orchard tools register unxip --path /path/to/unxip \
  --source-revision 6c3990517fcc4c1db6952fccf4c562fb14097601 --execute
orchard sdk import --input /path/to/Xcode.xip --arch x86_64 --json
orchard sdk import --input /path/to/Xcode.xip --arch x86_64 --execute
orchard sdk status --execute --json
```

Use `x86_64` for a Linux amd64 host and `arm64` for a Linux arm64 host. An
extracted `Xcode.app` directory is also accepted. Keep the same explicit XDG
configuration across import and later builds. Orchard extracts into fresh
private state, stages the SDK and Clang headers with user ownership, and calls
native `swift sdk install`. Existing SDKs are preserved; replacement is refused.
The [toolchain decisions](linux-toolchain-decisions.md) explain why the upstream
`xtool sdk install` path is not used.

## Inspect and run actions

Every operation is planned first. A plan ID covers validated inputs, project
manifest/source/config content, referenced IPA bytes, and relevant tool state.

```sh
orchard plan build --project ./Garden
orchard run build --project ./Garden --execute

orchard plan install --project ./Garden --device DEVICE_UDID --ipa ./Garden.ipa
orchard run install --project ./Garden --device DEVICE_UDID --ipa ./Garden.ipa --execute --confirm
```

`run` always requires `--execute`. App Store account writes and xtool
install/dev-run operations also require `--confirm`, because xtool may provision
or development-sign the app and change its signing identity. It must not be used
to claim preservation of an App Store distribution signature. Browser callers
submit only action/project/input data to `/api/plan`
and later submit a server-held `planId` to `/api/run`; they cannot supply an
executable or argv. Changed sources, manifests, tool state, or IPA content make a
stored plan stale and require a new inspection. A server-held plan is consumed
atomically by its first run attempt, successful or failed; an explicit planning
request is required before another attempt.

Fingerprints stream regular files through no-follow, workspace-confined opens.
Each path, byte count, and per-file SHA-256 is length-framed before it enters the
plan fingerprint. They bind a selected IPA even when it is below skipped
build-state directories, and bind planned executable bytes when the executable
is an accessible regular file. Source inputs are limited to 64 MiB each and 512
MiB total, selected IPAs to 8 GiB, and tool executables to 256 MiB. This content
binding detects local replacement between planning attempts; checksum
verification for Orchard-managed tool installation remains the bootstrap
layer's responsibility.

| Action | Effect | Command boundary |
| --- | --- | --- |
| `setup` | local read | `swift --version`; `xtool sdk status`; manual Xcode archive guidance |
| `build` | local build | `xtool dev build --configuration debug` |
| `devices` | device read | `xtool devices --no-wait` |
| `install` | confirmed device/signing write | `xtool install --udid DEVICE PATH`, or `xtool dev run --configuration debug --udid DEVICE` without `--ipa`; may provision/development-sign and change signing identity |
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
    "versionId": "a1b2c3d4-1111-4222-8333-abcdef123456",
    "buildId": "b2c3d4e5-2222-4333-8444-bcdefa234567"
  }
}
```

`appId` is numeric. ASC version/build resource IDs may be numeric or safe
UUID/resource strings; flag-like values, path separators, controls, and
overlong IDs are rejected. These are identifiers, not credentials.

ASC authentication remains in its private user configuration. Credential-free
tool probes, Swift/build tools, ASC operations, and `xdg-open` each receive a
separate allowlisted environment. Only ASC operations inherit `ASC_*`; the
browser opener retains display/session variables without account credentials.
Build hooks still run with the invoking user's authority—this environment
filter is not an operating-system sandbox.

Managed processes use Linux process groups, deadlines, and bounded pipe waits so
descendants cannot keep an operation or version probe alive indefinitely.
Output is redacted for PEM blocks, bearer/JWT tokens, known credential environment
values, and presigned URL queries before the bounded value reaches CLI/API output
or private history storage. `orchard.json` is limited to 1 MiB, and history reads
enforce an 8 MiB streaming limit while rejecting nonregular files. If an operation
runs and a later history append fails because the file changed concurrently, JSON
returns a `history_failed` error with the bounded, redacted operation in
`error.result`; human output prints that result before the receipt-storage error.

## Verification boundary

The Go tests prove Orchard's local validation, planning, process control,
history, CLI envelopes, and loopback HTTP protections. They do not prove an iOS
SDK installation, Apple account access, distribution signing, a physical-device
run, upload processing, or App Review submission. Those require separate trusted
host, hardware, and account evidence.
