# Linux setup and SDK import

Orchard can install the open-source tools listed in `toolchains.lock.json` into
private XDG directories. Planning is the default; `--execute` is always a
separate, explicit gate.

```sh
orchard tools install asc --json
orchard tools install asc --execute --json
orchard tools install xtool --execute
orchard tools install zsign --execute
```

Each plan reports the pinned version, destination, HTTPS source URL, SHA-256,
and download size. Execution delegates to Orchard's reviewed bootstrap
installer, verifies the complete installed tree and active link, and never uses
`sudo`, changes the global `PATH`, or runs a remote shell script. `orchard
doctor` and `orchard tools` only perform bounded read-only discovery; they do
not download or repair tools.

Install Swift 6.3 or later using the [official Linux instructions](https://www.swift.org/install/linux/),
then check `swift --version`. Linux runtime
libraries, `usbmuxd`, device permissions, pairing, Developer Mode, Apple
account access, and applicable Apple license terms remain explicit operator
prerequisites.

## AssetKit bridge and XIP helper

Build Orchard's pinned Linux AssetKit bridge from this checkout:

```sh
swift build --package-path tools/asset-compiler -c release
orchard_assets=$(realpath "$PWD/tools/asset-compiler/.build/release/orchard-assets")
orchard tools register orchard-assets \
  --path "$orchard_assets" \
  --assetkit-revision e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7 \
  --execute
```

The registration records the exact executable SHA-256 in private user state.
Use the concrete path: SwiftPM's `.build/release` directory is a symlink,
and registration rejects symbolic links in executable paths.
Orchard runs `--help` because the bridge intentionally has no `--version`.
Source and AssetKit revisions are retained only when the operator supplies
them; Orchard does not invent provenance from a path or binary.

Full XIP extraction on Linux uses standalone
[unxip](https://github.com/saagarjha/unxip) 3.3 at reviewed revision
`6c3990517fcc4c1db6952fccf4c562fb14097601`. Build it on Linux with Swift
6.3.3, `liblzma-dev`, and `zlib1g-dev`, or use a locally packaged executable,
then register it:

```sh
orchard tools register unxip --path /absolute/path/unxip \
  --source-revision 6c3990517fcc4c1db6952fccf4c562fb14097601 \
  --execute
```

Packaged `/usr/local/bin/orchard-assets` and `/usr/local/bin/unxip` files are
reported but remain unverified unless a matching packaged or private SHA-256
receipt is present.

## Import an operator-supplied Apple SDK

Open [Apple's Xcode downloads](https://developer.apple.com/download/all/?q=Xcode)
in your Linux browser, sign in, and download the Xcode 26 XIP. This is the
authenticated download route documented by [xtool's Linux guide](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/Installation-Linux.md).
The file is an archive input; Xcode does not run during import or compilation.
Review the [SDK terms and provenance boundary](toolchain-research.md#sdk-license-boundary).

Orchard never downloads Xcode or Apple SDK content. Supply the downloaded XIP
or an already extracted `Xcode.app` directory:

```sh
orchard sdk status --execute --json
orchard sdk import --input "$HOME/Downloads/Xcode.xip" --arch x86_64 --json
orchard sdk import --input "$HOME/Downloads/Xcode.xip" --arch x86_64 --execute --json
```

Replace `Xcode.xip` with the downloaded filename. Use `x86_64` for a Linux
amd64 host or `arm64` for a Linux arm64 host. Leave space for the archive,
extracted Xcode files, staged SDK and installed SDK; failed staging trees are
preserved for diagnosis.

For XIP input, Orchard runs `unxip --statistics INPUT.xip FRESH_OUTPUT_DIR`,
discovers the extracted `Xcode.app`, then uses xtool 1.19's exact contract:

```text
xtool sdk build INPUT OUTPUT_PARENT --arch arm64|x86_64
clang -print-resource-dir
Orchard copies OUTPUT_PARENT/darwin.xtoolsdk into a fresh user-owned darwin.artifactbundle
Orchard copies CLANG_RESOURCE_DIR/include into that staged artifact bundle
swift sdk install ABSOLUTE_FRESH_DARWIN_ARTIFACTBUNDLE
xtool sdk status
swift sdk list
swift sdk configure darwin arm64-apple-ios --show-configuration
```

Every extraction and build uses a fresh private staging path. Failed output is
retained for diagnosis and an existing Darwin SDK is never replaced: Orchard
checks `xtool sdk status` while planning and again immediately before the Swift
install call. The selected Swift and Clang executables, Clang header tree, and
configuration root are fingerprinted before execution. Orchard never invokes
the normal-user-incompatible `xtool sdk install` copy path and never uses
`sudo`. Exit zero is insufficient when status says `Not installed`. Orchard
also requires Swift to list `darwin`, resolves the configured
`arm64-apple-ios` `sdkRootPath`, and checks that it agrees with the
schema-version 4.0 `swift-sdk.json` inside the installed artifact bundle. The
active SDK root is the selected `iPhoneOS*.sdk` directory, not the enclosing
artifact bundle.

Swift SDK persistence is rooted at one explicit absolute `XDG_CONFIG_HOME` for
tool discovery, status, import, and project builds. Orchard honors an absolute
configured value; otherwise it uses the invoking user's absolute `~/.config`.
The SDK is expected at `XDG_CONFIG_HOME/swiftpm/swift-sdks/darwin.artifactbundle`.
Changing `HOME` does not redirect this workflow to a different SDK root.

Receipts identify the input as `operator_supplied`, record its SHA-256, and
retain values only from actual Xcode, platform, SDK settings, and SDK system
metadata. Named metadata file hashes and their exact field mappings are kept in
private state. The exact bounded named metadata bytes are copied beneath the
receipt's `metadataSnapshotRoot`, using the same slash-relative paths as the
`metadataHashes` keys, and are hash-verified so the receipt remains useful
after an operator-provided Xcode mount is removed. SDKSettings `Version` is `sdkVersion`; SystemVersion
`ProductVersion` is retained separately as `sdkProductVersion` and never used
as a substitute. Missing XIP-filtered metadata remains explicitly missing. A matching
hash or successful extraction does not authenticate Apple's XIP signature.
Installing tools or an SDK does not prove an iOS build, physical-device
operation, signing validity, distribution readiness, or App Store acceptance.
