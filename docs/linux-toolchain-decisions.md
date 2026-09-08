# Decisions from real Linux builds

The primary workflow uses Linux executables throughout. These decisions come from running the pinned upstream tools with an actual Apple SDK, rather than assuming their command help establishes a working release path. The [verification record](verification.md) identifies which changes have been exercised and which still await final integration.

## Preserve SDK provenance before extraction filters it

xtool's SDK builder retains the SDK content it needs, but it does not preserve every original Xcode metadata file. Extract the supplied XIP with the separately built, pinned unxip executable first. Capture the relevant metadata and its source hashes before invoking the SDK builder in a fresh destination.

The actual archive used for the probe distinguishes these values:

| Meaning | Source | Observed value |
| --- | --- | --- |
| Xcode version/build | `Xcode.app/Contents/version.plist` | 26.6 / 17F113 |
| Platform version/build | `Contents/Developer/Platforms/iPhoneOS.platform/version.plist` | 26.5 / 23F81a |
| SDK version/name | iPhoneOS SDK `SDKSettings.json` | 26.5 / iphoneos26.5 |
| SDK system version/build | SDK `System/Library/CoreServices/SystemVersion.plist` | 26.5.1 / 23F81a |

The SDK version and system product version are different facts. Do not substitute one for the other or invent missing DT metadata. Retaining Xcode archive provenance also does not mean Xcode executed the build. No Apple SDK is redistributed with Orchard.

## Install as the current Linux user

In the tested environment, `xtool sdk install` tried to preserve root ownership while copying system Clang headers through Swift Foundation. It failed with `EPERM` under an ordinary user. Running as root would defeat the intended installation model.

The verified alternative stages the generated Darwin artifact bundle and the selected host Clang resource headers as user-owned files, then calls native `swift sdk install`. Discover the resource directory from that Clang executable with `-print-resource-dir`; do not hard-code its version or location. Keep SDK symlinks contained and preserve their intended relationships while copying.

SwiftPM's default SDK location can follow the passwd home directory rather than the `HOME` environment variable. Explicit, consistent `XDG_CONFIG_HOME` isolated installation, discovery and builds under the persistent container state in the real test. Query the active Swift SDK configuration instead of guessing its path.

## Give Clang the actual Darwin SDK root

Swift 6.3.3's Linux link invocation passed `--sysroot`, but Clang's Darwin SDK metadata reader uses `-isysroot`. Without that metadata, it recorded the target minimum as the SDK version: the first real app reported minimum iOS 17 and SDK 17 even though compilation used SDK 26.5.

Passing the same verified SDK directory with `-Xclang-linker -isysroot -Xclang-linker SDK_ROOT` fixed this in a complete xtool rebuild. The resulting Mach-O reported minimum iOS 17 and SDK 26.5. The SwiftPM target's linker settings can carry these flags into xtool's generated executable. Bind the path to the active imported SDK; do not repair the result by stamping an arbitrary SDK version into the binary.

The behavior follows Clang's [Darwin toolchain implementation](https://github.com/swiftlang/llvm-project/blob/82cdc19fa54d566969527b56f587ea8ea30bef51/clang/lib/Driver/ToolChains/Darwin.cpp#L2477).

## Compile assets before signing

The separate Swift AssetKit bridge compiles catalogs on Linux. The real 18-slot icon test produced 15 distinct PNG files and `Assets.car`; identical filenames shared by phone/tablet slots are coalesced. Staging removes only the old root signing artifacts that export replaces, then compiles assets and signs the staged bundle. The original source bundle remains untouched.

## Package the signed bytes correctly

Pinned zsign 1.1.2 requires an archive root containing `Payload/NAME.app` when producing an IPA. A real invocation outside that layout signed successfully and then failed to archive. Its [packaging implementation](https://github.com/zhlynn/zsign/blob/v1.1.2/src/zsign.cpp#L479-L498) archives the directory containing `Payload`, so private identity files and scratch/output paths must stay outside that directory.

The tested zsign archive also reported DOS-style `0666` modes for every member. Canonical packaging preserves the already-signed file bytes while recording executable/directory modes as `0755` and resource modes as `0644`. Reinspect the canonical IPA and publish it without replacing an existing output.

These corrections establish local build and packaging behavior. Physical-device launch and successful Apple processing remain separate evidence gates.
