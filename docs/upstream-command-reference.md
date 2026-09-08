# Upstream command reference

Captured from checksum-verified upstream Linux arm64 release binaries on Debian 12 on 2026-09-08. These help checks used no Apple credentials or network. This is command-contract proof, not build, device, or App Store proof.

## asc --version

Additional version check from the pinned ASC 5.0.0 binary in the Linux arm64
browser-test container on 2026-09-08. Exit status: 0. Binary SHA-256:
`f457c466e869bcf1824795f493d4b9169644d7784f0dd124dedd08461c3d5099`.

```text
5.0.0 (commit: 500a36b, date: 2026-09-06T20:09:08+07:00)
```

This executable does not prefix its version with `asc version`. Compatibility
checks must parse the leading version rather than require that label.

## xtool --version

Exit status: 0

```text
xtool 1.19.0
```

## xtool new --help

Exit status: 0

```text
OVERVIEW: Create a new xtool SwiftPM project

USAGE: xtool new [<name>] [--skip-setup]

ARGUMENTS:
  <name>

OPTIONS:
  --skip-setup            Skip setup steps. (default: false)
        By default, this command first invokes `xtool setup` to complete any
        missing setup steps, like authenticating and installing the SDK. Use
        this flag to always skip setup.
  --version               Show the version.
  -h, --help              Show help information.
```

## xtool dev build --help

Exit status: 0

```text
OVERVIEW: Build app with SwiftPM

This command builds the SwiftPM-based iOS app in the current directory

USAGE: xtool dev build [--configuration <configuration>] [--sign] [--ipa] [--triple <triple>]

OPTIONS:
  -c, --configuration <configuration>
                          Build with configuration (values: debug, release;
                          default: debug)
  -s, --sign              Codesign the built app
  -i, --ipa               Output a .ipa file instead of a .app
  --triple <triple>       Custom target triple to build for
        Defaults to 'arm64-apple-ios'
  --version               Show the version.
  -h, --help              Show help information.
```

## xtool dev run --help

Exit status: 0

```text
OVERVIEW: Build and run app with SwiftPM

This command deploys the SwiftPM-based iOS app in the current directory

USAGE: xtool dev run [--configuration <configuration>] [--udid <udid>] [--usb] [--network] [--all]

OPTIONS:
  -c, --configuration <configuration>
                          Build with configuration (values: debug, release;
                          default: debug)
  -u, --udid <udid>
  --usb/--network/--all   (default: --all)
  --version               Show the version.
  -h, --help              Show help information.
```

## xtool install --help

Exit status: 0

```text
OVERVIEW: Install an ipa file to your device

USAGE: xtool install [--udid <udid>] [--usb] [--network] [--all] <path>

ARGUMENTS:
  <path>                  The path to a custom app/ipa to install

OPTIONS:
  -u, --udid <udid>
  --usb/--network/--all   (default: --all)
  --version               Show the version.
  -h, --help              Show help information.
```

## xtool devices --help

Exit status: 0

```text
OVERVIEW: List devices

USAGE: xtool devices [--usb] [--network] [--all] [--wait] [--no-wait]

OPTIONS:
  --usb/--network/--all   Which devices to search for (default: --all)
  --wait/--no-wait        If no devices are found at first, wait until at least
                          one is connected. (default: --wait)
  --version               Show the version.
  -h, --help              Show help information.
```

## asc builds upload --help

Exit status: 0

```text
DESCRIPTION
  Upload a build to App Store Connect.

USAGE
  asc builds upload [flags]

By default, this command uploads the IPA/PKG to the presigned URLs and commits
the file immediately. Use --verify-timeout to briefly watch for immediate
post-commit processing failures, or --wait for full build discovery and
processing.
When --test-notes is set, the command waits only until the build appears, then
creates or updates the requested localization. Add --wait when the invocation
must also wait for processing to complete.
Use --dry-run to only reserve the upload operations.
Presigned URLs and request-header values are redacted from output by default.
Pass --include-sensitive only when another tool must consume those capabilities.

Use --ipa for iOS, tvOS, and visionOS apps. Its platform is detected from the
top-level app Info.plist when available, with IOS retained as the compatibility
default for older archives without platform metadata. Use --pkg for macOS apps;
its platform is automatically set to MAC_OS.

Examples:
  asc builds upload --app "123456789" --ipa "path/to/app.ipa"
  asc builds upload --ipa "app.ipa" --version "1.0.0" --build-number "123"
  asc builds upload --app "123456789" --ipa "app.ipa" --dry-run
  asc builds upload --app "123456789" --ipa "app.ipa" --dry-run --include-sensitive
  asc builds upload --app "123456789" --ipa "app.ipa" --test-notes "Test flow" --locale "en-US"
  asc builds upload --app "123456789" --pkg "path/to/app.pkg" --version "1.0.0" --build-number "123"

FLAGS
  --app          App Store Connect app ID; IPA uploads also accept an exact bundle ID or exact name (required, or ASC_APP_ID env)
  --build-number CFBundleVersion (e.g., 123, auto-extracted from IPA if not provided)
  --checksum     Verify upload checksums if provided by API (default: false)
  --concurrency  Upload concurrency (default: 4)
  --dry-run      Reserve upload operations without uploading the file (default: false)
  --include-sensitive [experimental] Print secret values such as demo account passwords instead of "(redacted)"; applies only to this invocation (default: false)
  --ipa          Path to .ipa file (for iOS, tvOS, visionOS apps)
  --locale       Locale for --test-notes (e.g., en-US)
  --output       Output format: json, table, markdown (default: json)
  --pkg          Path to .pkg file (for macOS apps)
  --platform     Platform: IOS, MAC_OS, TV_OS, VISION_OS (auto-detected for --ipa and --pkg)
  --poll-interval Polling interval for --wait and --test-notes (default: 30s)
  --pretty       Pretty-print JSON output (default: false)
  --test-notes   What to Test notes (waits for build discovery)
  --verify-timeout How long to watch for immediate post-commit upload failures (0 to disable) (default: 0s)
  --version      CFBundleShortVersionString (e.g., 1.0.0, auto-extracted from IPA if not provided)
  --wait         Wait for build processing to complete (default: false)
```

## asc submit create --help

Exit status: 2

```text
Error: `asc submit create` was removed. Use `asc review submit` for already-uploaded builds, or `asc publish appstore --submit` for the full shipping path.
DESCRIPTION
  Submission lifecycle tools; use `publish appstore --submit` to ship.

USAGE
  asc submit <subcommand> [flags]

Submission lifecycle tools for App Store review.

Use:
  - asc publish appstore --submit for the canonical high-level App Store shipping path
  - asc validate for canonical readiness checks before submission
  - asc submit status/cancel for lower-level review submission lifecycle work

SUBCOMMANDS
  status  Check submission status.
  cancel  Cancel a submission.
```

## asc validate --help

Exit status: 0

```text
DESCRIPTION
  Canonical App Store submission readiness report.

USAGE
  asc validate --app "APP_ID" (--version-id "VERSION_ID" | --version "VERSION") [flags]

Validate pre-submission readiness for an App Store version.

This is the canonical command for App Store submission readiness.
Use it instead of `asc submit preflight`.

The default validate response includes an ordered remediation plan, so the first step is the next thing to fix.

Placeholder checks preserve shorter Lorem Ipsum product wording and ordinary
localized TODO copy without marker punctuation; only template-like residue is
reported.

Checks:
  - Metadata length limits
  - Placeholder copy in localized listing fields (warning; --strict to block)
  - Deterministic metadata content and keyword hygiene warnings
  - Optional bounded checks for public metadata URL destinations (--check-urls)
  - Required fields and localizations
  - App Store review details completeness
  - Primary category configured
  - Build attached and processed
  - Build encryption declaration readiness
  - App content rights declaration
  - Pricing schedule and territory availability
  - Screenshot presence and size compatibility
  - Subscription review readiness and promotional image guidance
  - Age rating completeness

Experimental deep validation:
  --deep adds read-only checks from an existing cached Apple web session for
  App Privacy publication, required agreements, and first-of-type subscription
  attachment. It also classifies every actionable finding as api-fixable,
  web-fixable, or manual and returns exact available commands and App Store
  Connect links. Deep validation never starts an interactive login.

Examples:
  asc validate --app "APP_ID" --version-id "VERSION_ID"
  asc validate --app "APP_ID" --version "1.0.0" --platform IOS
  asc validate --app "APP_ID" --version-id "VERSION_ID" --platform IOS --output table
  asc validate --app "APP_ID" --version-id "VERSION_ID" --strict
  asc validate --app "APP_ID" --version-id "VERSION_ID" --deep
  asc validate --app "APP_ID" --version-id "VERSION_ID" --check-urls
  asc validate --app "APP_ID" --version "1.0.0" --deep --apple-id "user@example.com"

TestFlight:
  asc validate testflight --app "APP_ID" --build-id "BUILD_ID"

In-App Purchases:
  asc validate iap --app "APP_ID"

Subscriptions:
  asc validate subscriptions --app "APP_ID"

SUBCOMMANDS
  testflight     Validate TestFlight build readiness before distribution.
  iap            Validate IAP review readiness (warning-only by default).
  subscriptions  Validate subscription metadata, screenshot delivery, pricing, and availability.

FLAGS
  --app          App Store Connect app ID (or ASC_APP_ID)
  --apple-id     [experimental] Cached Apple web session to use with --deep
  --check-urls   [experimental] Check metadata URL destinations with bounded public HTTP requests (default: false)
  --deep         [experimental] Verify blockers that require a cached Apple web session (default: false)
  --output       Output format: json, table, markdown (default: json)
  --platform     Platform: IOS, MAC_OS, TV_OS, VISION_OS
  --pretty       Pretty-print JSON output (default: false)
  --strict       Treat warnings as errors (exit non-zero) (default: false)
  --version      App Store version string
  --version-id   App Store version ID
```

## zsign --help

Exit status: 255

```text
zsign (v1.1.2) is a codesign alternative for iOS12+ on macOS, Linux and Windows. 
Visit https://github.com/zhlynn/zsign for more information.

Usage: zsign [-options] [-k privkey.pem] [-m dev.prov] [-o output.ipa] file|folder
options:
-k, --pkey		Path to private key or p12 file. (PEM or DER format)
-m, --prov		Path to mobile provisioning profile.
-c, --cert		Path to certificate file. (PEM or DER format)
-a, --adhoc		Perform ad-hoc signature only.
-d, --debug		Generate debug output files. (.zsign_debug folder)
-f, --force		Force sign without cache when signing folder.
-o, --output		Path to output ipa file.
-p, --password		Password for private key or p12 file.
-b, --bundle_id		New bundle id to change.
-n, --bundle_name	New bundle name to change.
-r, --bundle_version	New bundle version to change.
-e, --entitlements	New entitlements to change.
-I, --icon		Path to new app icon to replace the primary icon. (PNG format)
-z, --zip_level		Compressed level when output the ipa file. (0-9)
-l, --dylib		Path to inject dylib file. Use -l multiple time to inject multiple dylib files at once.
-D, --rm_dylib		Name of dylib to remove. Use -D multiple times to remove multiple dylibs at once.
-w, --weak		Inject dylib as LC_LOAD_WEAK_DYLIB.
-i, --install		Install ipa file using ideviceinstaller command for test.
-t, --temp_folder	Path to temporary folder for intermediate files.
-2, --sha256_only	(Deprecated, now the default.) Kept for backward compatibility.
-L, --legacy_sha1	Emit a dual SHA1+SHA256 CodeDirectory for iOS <= 10 compatibility.
-C, --check		Check certificate validity and OCSP revocation status.
-q, --quiet		Quiet operation.
-x, --metadata		Extract metadata and icon to the specified directory.
-R, --rm_provision	Remove mobileprovision file after signing.
-S, --enable_docs	Enable UISupportsDocumentBrowser and UIFileSharingEnabled.
-M, --min_version	Set MinimumOSVersion in Info.plist.
-E, --rm_extensions	Remove all app extensions (PlugIns/Extensions).
-W, --rm_watch		Remove watch app from the bundle.
-U, --rm_uisd		Remove UISupportedDevices from Info.plist.
-P, --inject_extensions	Also inject -l dylibs into app extensions (PlugIns/Extensions).
-v, --version		Shows version.
-h, --help		Shows help (this message).
```

## asc review submit --help

Exit status: 0

```text
DESCRIPTION
  Attach a build and submit an already-prepared App Store version for review.

USAGE
  asc review submit [flags]

This is the easier modern wrapper around:
  - asc versions attach-build
  - asc review submissions-create
  - asc review items-add
  - asc review submissions-submit

Examples:
  asc review submit --app "123456789" --version "1.2.3" --build-id "BUILD_ID" --confirm
  asc review submit --app "123456789" --version-id "VERSION_ID" --build-id "BUILD_ID" --dry-run

FLAGS
  --app          App Store Connect app ID (or ASC_APP_ID)
  --build-id     Build ID to attach
  --confirm      Confirm submission (required unless --dry-run) (default: false)
  --dry-run      Preview the review submission flow without mutating (default: false)
  --output       Output format: json, table, markdown (default: json)
  --platform     Platform: IOS, MAC_OS, TV_OS, VISION_OS (default: IOS)
  --pretty       Pretty-print JSON output (default: false)
  --version      App Store version string
  --version-id   App Store version ID
```

## asc profiles inspect --help

Exit status: 0

```text
DESCRIPTION
  Inspect a local provisioning profile.

USAGE
  asc profiles inspect --path "./profile.mobileprovision" [flags]

This command decodes the embedded plist from a .mobileprovision file and prints
the profile identifiers, dates, certificate fingerprints, devices, and
entitlements.

Examples:
  asc profiles inspect --path "./profile.mobileprovision"
  asc profiles inspect --path "./profile.mobileprovision" --output json
  asc profiles inspect --path "./profile.mobileprovision" --entitlements

FLAGS
  --entitlements Include entitlement key/value rows in table or markdown output (default: false)
  --output       Output format: json, table, markdown (default: json)
  --path         Path to a .mobileprovision file to inspect
  --pretty       Pretty-print JSON output (default: false)
```

## xtool sdk build --help

Exit status: 0

```text
OVERVIEW: Build the Darwin SDK from Xcode.xip

USAGE: xtool sdk build <path> <output-dir> [--arch <arch>]

ARGUMENTS:
  <path>                  Path to Xcode.xip or Xcode.app
  <output-dir>            Output directory

OPTIONS:
  --arch <arch>           The architecture of the Linux host the SDK is being
                          built for. (default: auto)
        Defaults to 'auto', which attempts to match the current host
        architecture.
  --version               Show the version.
  -h, --help              Show help information.
```
