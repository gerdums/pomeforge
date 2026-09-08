# Linux iOS toolchain research

Verified against upstream documentation and source on 2026-09-08. This is a technical implementation plan, not a claim that Orchard has built, installed, uploaded, or passed App Review with an app. Public repository source was inspected without using Apple credentials.

## What can run on Linux

The components exist for a native pipeline: Swift and xtool compile a SwiftPM iOS app; AssetKit creates its asset catalog; ASC creates signing resources; zsign applies distribution signatures; ASC uploads the resulting IPA over HTTP and submits an already prepared App Store version. The integration work is real, especially build provenance metadata, signing validation, and evidence from Apple processing a resulting binary.

| Stage | Linux component | Evidence and boundary |
| --- | --- | --- |
| Generate SwiftUI app | xtool | SwiftPM library product with a SwiftUI `@main` entry point |
| Compile iOS code | Swift 6.3 plus xtool | Uses a Darwin Swift SDK extracted from the user's Xcode download; SDK license issue below |
| Compile app icons/assets | AssetKit | Standalone MIT library, not yet wired into stable xtool |
| Register IDs, issue certificates/profiles | ASC public API commands | Private key generated locally; a downloaded certificate alone cannot sign |
| Sign development build | xtool or zsign | xtool's built-in signer requests development profiles and may change bundle IDs |
| Sign distribution build | zsign | Native Linux signing with certificate, private key, entitlements, profile |
| Install and inspect physical device | usbmuxd, xtool, pymobiledevice3 | Physical trust, Developer Mode and supported developer services required |
| Screenshots, logs, device interaction | pymobiledevice3 | Current versions support Linux userspace tunnels for iOS 17.4+ |
| Upload IPA | ASC | Direct Go HTTP public `buildUploads`/`buildUploadFiles` workflow, no Transporter invocation |
| Metadata and App Review submission | ASC plus App Store Connect website | API covers much of the work; account agreements and some declarations remain web workflows |
| iOS Simulator, arbitrary Xcode build phases, unsupported Apple resource tools | No supported adapter | Build a Linux replacement where feasible; never fall back to macOS |

The product requirement is strict: every build step executes on Linux. Remote or hosted macOS is outside scope. Operation receipts should record the Linux toolchain and environment that actually produced each artifact.

## Version pins

Upstream release metadata currently reports:

| Tool | Version | Linux delivery |
| --- | --- | --- |
| [xtool](https://github.com/xtool-org/xtool/releases/tag/1.19.0) | 1.19.0 | `xtool-x86_64.AppImage`, `xtool-aarch64.AppImage` |
| [ASC](https://github.com/rorkai/App-Store-Connect-CLI/releases/tag/5.0.0) | 5.0.0 | `asc_5.0.0_linux_amd64`, `asc_5.0.0_linux_arm64` |
| [zsign](https://github.com/zhlynn/zsign/releases/tag/v1.1.2) | 1.1.2 | x86_64, aarch64, armv7 and static musl artifacts |
| [pymobiledevice3](https://github.com/doronz88/pymobiledevice3/releases/tag/v11.10.1) | 11.10.1 | PyPI package, Python 3.9+ |
| [AssetKit](https://github.com/xtool-org/AssetKit/tree/1.0.0) | 1.0.0 tag | Swift library source, no upstream CLI executable |

The inspected development heads were xtool `ae2bef7bbae65ff51a48e57d93c90ea8ec9f341f`, ASC `ebe622db4c8ba3a7a9f1e3f9d512bc5c84907bf0`, pymobiledevice3 `ce8a9c05d275a67a05c931bb8cfd98eecfeab77c`, and AssetKit `e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7`. AssetKit's 1.0.0 tag is `bc9912b1017facbda994cd24ce7e44b93165adc6`. Pin and test the exact revision used by an adapter instead of assuming head and the latest release have identical behavior.

AppImage covers packaging, not every Linux configuration. Check CPU architecture, Swift support, libc and FUSE availability; offer AppImage extraction where FUSE is absent. Omarchy can use the Arch host packages for usbmuxd and device permissions. Unsupported architectures should get a precise diagnostic, not an assertion of universal binary compatibility.

## xtool adapter

[Linux installation](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/Installation-Linux.md) requires Swift 6.3, usbmuxd and Xcode 26 `.xip`. The user obtains the XIP through Apple's authenticated [downloads page](https://developer.apple.com/download/all/?q=Xcode). xtool extracts it on Linux and installs a Darwin Swift SDK. Do not mirror or bundle Apple's SDK in Orchard's releases.

```sh
swift --version
xtool sdk install /path/to/Xcode.xip
xtool sdk status
swift sdk list
xtool new Hello --skip-setup
```

`--skip-setup` prevents `new` from starting interactive Apple authentication and SDK setup. `xtool setup` offers public ASC API-key authentication for paid accounts, or Apple ID/password/2FA through private APIs. Use the public-key route for the normal paid-developer workflow. Secrets go to tools through private files or interactive input, never Orchard plan JSON or logs.

From inside the generated project:

```sh
xtool dev build --configuration debug
xtool dev build --configuration release --ipa
xtool dev build --configuration release --ipa --sign
xtool devices --no-wait
xtool dev run --configuration debug --udid DEVICE_UDID
xtool install --udid DEVICE_UDID /path/to/App.ipa
xtool launch --udid DEVICE_UDID com.example.Hello
```

`build` accepts `--triple`, defaulting to `arm64-apple-ios`. An ordinary build writes `xtool/PRODUCT.app`; `--ipa` writes `xtool/PRODUCT.ipa`. `--sign` is development signing. The source requests `certificateType: development` and `profileType: iosAppDevelopment`, even with `--configuration release`. `xtool install` invokes the integrated provisioning/signing installer, so use a plain installation tool to preserve a previously chosen signing identity. Device commands can wait indefinitely; Orchard must set timeouts and select a UDID explicitly in automation.

Sources: [build command](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/DevCommand.swift), [profile issuance](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XKit/DeveloperServices/Profiles/DeveloperServicesFetchProfileOperation.swift), [certificate issuance](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XKit/DeveloperServices/Certificates/DeveloperServicesFetchCertificateOperation.swift), [installation command](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/InstallCommand.swift).

### Exact SwiftPM project shape

The [upstream generator](https://github.com/xtool-org/xtool/blob/1.19.0/Sources/XToolSupport/NewCommand.swift) exposes the app as a library product. Do not change this into a conventional SwiftPM executable product.

```swift
// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "Hello",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [.library(name: "Hello", targets: ["Hello"])],
    targets: [.target(name: "Hello")]
)
```

`Sources/Hello/HelloApp.swift`:

```swift
import SwiftUI

@main
struct HelloApp: App {
    var body: some Scene {
        WindowGroup { Text("Hello from Linux") }
    }
}
```

`xtool.yml`:

```yaml
version: 1
bundleID: com.example.Hello
infoPath: Info.plist
```

`.sourcekit-lsp/config.json`:

```json
{"swiftPM":{"swiftSDK":"arm64-apple-ios"}}
```

The planner supplies `UIDeviceFamily: [1, 2]`, iPhone portrait and all iPad orientations by default. Override `CFBundleShortVersionString`, `CFBundleVersion`, privacy permission descriptions and app-specific properties through `infoPath`. Other supported configuration keys are `resources`, `iconPath` and `entitlementsPath`. [Configuration guide](https://github.com/xtool-org/xtool/blob/1.19.0/Documentation/xtool.docc/Control.md).

Native scope starts with SwiftUI/UIKit applications and supported SwiftPM dependencies. Stable xtool does not supply `actool`, Interface Builder storyboard/XIB compilation, a Metal shader compiler, an iOS Simulator, or a replacement for arbitrary Xcode build phases. These are resource/build-system limitations, not a statement that all APIs in those frameworks are inaccessible. Classify dependencies and source resources before running the pipeline. Existing React Native, Flutter, CocoaPods or arbitrary Xcode projects need separate adapters and evidence.

## AssetKit closes the asset-catalog gap

xtool's [open PR 219](https://github.com/xtool-org/xtool/pull/219) moved its clean-room compiler into [xtool-org/AssetKit](https://github.com/xtool-org/AssetKit). Current source builds `Assets.car` on Linux and emits the app-icon plist additions and loose PNG files needed by SpringBoard. It supports PNG app icons, PNG/JPEG/SVG images and color sets. PDF vectors, data/sticker sets, AR objects and non-iOS variants remain outside its documented support. SVG input needs `rsvg-convert`; PNG app icons do not.

The package product is `AssetKit`. Its current manifest requires Swift tools 6.3, with `swift-png` and vendored BSD-licensed LZFSE C sources. Orchard can ship a small executable package depending on a reviewed revision:

```swift
// swift-tools-version: 6.3
import PackageDescription

let package = Package(
    name: "OrchardAssets",
    products: [.executable(name: "orchard-assets", targets: ["OrchardAssets"])],
    dependencies: [
        .package(url: "https://github.com/xtool-org/AssetKit",
                 revision: "e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7")
    ],
    targets: [
        .executableTarget(name: "OrchardAssets", dependencies: [
            .product(name: "AssetKit", package: "AssetKit")
        ])
    ]
)
```

The executable accepts catalog and staged `.app` paths, then runs:

```swift
import AssetKit
import Foundation

let result = try await XCAssetCompiler(deploymentTarget: "17.0")
    .compile(catalog: catalogURL)
try result.carData.write(to: appURL.appendingPathComponent("Assets.car"))
if let icons = result.appIconBundle {
    for file in icons.looseFiles {
        try file.data.write(to: appURL.appendingPathComponent(file.name))
    }
    // Merge icons.infoPlistAdditions into the existing Info.plist, preserving
    // its unrelated values. Validate loose-file names before writing them.
}
```

Build the Linux host executable with `swift build --package-path tools/asset-compiler -c release`. Orchard's implemented bridge accepts `orchard-assets compile --catalog CATALOG.xcassets --app STAGED.app --minimum-ios 17.0 --json`. That interface is Orchard's adapter contract, not an upstream AssetKit command. Compilation must finish before signing, because editing Info.plist or resources invalidates signatures.

AssetKit documents deterministic Linux/macOS bytes and a macOS CI check using Apple's `assetutil`. Those checks establish format parsing for fixtures, not acceptance of every app by App Store Connect. Retain an app-icon device screenshot and a successful build-processing receipt for the actual release candidate. [Compiler API](https://github.com/xtool-org/AssetKit/blob/e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7/Sources/AssetKit/XCAssetCompiler.swift).

## Distribution signing from Linux

Use ASC for certificate/provisioning resource management and zsign for binary signing. ASC's `signing resign` and temporary-keychain workflow explicitly require macOS, so they cannot be used as the Linux signer. [Source restriction](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/internal/cli/signing/signing_resign_environment.go).

The following are command contracts for user-authorized account operations. Use private state directories outside the project and read IDs from the returned JSON.

```sh
asc auth login --bypass-keychain --name orchard \
  --key-id KEY_ID --issuer-id ISSUER_ID --private-key /private/AuthKey.p8
asc auth status --validate
asc bundle-ids create --identifier com.example.Hello --name Hello \
  --platform IOS --output json
asc certificates csr generate --key-out /private/distribution.key \
  --csr-out /private/distribution.csr --output json
asc certificates create --certificate-type IOS_DISTRIBUTION \
  --csr /private/distribution.csr --output json
asc profiles create --name "Hello App Store" --profile-type IOS_APP_STORE \
  --bundle BUNDLE_RESOURCE_ID --certificate CERTIFICATE_RESOURCE_ID --output json
asc profiles download --id PROFILE_RESOURCE_ID --output /private/app.mobileprovision
asc profiles inspect --path /private/app.mobileprovision --output json
```

`--bundle`, `--certificate` and `--id` above take API resource IDs, not the bundle identifier string. App Store profiles omit devices. For physical-device builds, issue a development or Ad Hoc profile with the registered devices. Private signing keys are generated locally and cannot be recovered from ASC after issuance. A lost/ambiguous certificate-create response must be reconciled against the preserved CSR/public key before another create request. [ASC signing guide](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/guides/code-signing.mdx), [profile commands](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/commands/profiles.mdx).

OpenSSL is an alternative CSR generator:

```sh
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out /private/distribution.key
openssl req -new -sha256 -key /private/distribution.key \
  -out /private/distribution.csr -subj '/CN=Orchard Distribution'
```

Decode `.data.attributes.certificateContent` from ASC's certificate response to DER, then convert it to PEM with `openssl x509 -inform DER -in certificate.cer -out certificate.pem`. Never put private key bytes in a JSON response or build receipt.

zsign's documented flags support a separate key and certificate:

```sh
zsign -k /private/distribution.key -c /private/certificate.pem \
  -m /private/app.mobileprovision -e /private/app-entitlements.plist \
  -o dist/Hello.ipa private-stage/archive/Payload/Hello.app
zsign -C dist/Hello.ipa
```

For zsign 1.1.2 to create an IPA, the app must be beneath a `Payload` directory. It archives the directory containing `Payload`, so that archive root must contain only intended IPA content. Keep key/certificate files, entitlements, catalog scratch data, and the output IPA outside it. A Linux run with the pinned binary reproduced a successful signature followed by “Can't find payload directory!” when this layout was absent. [Pinned packaging implementation](https://github.com/zhlynn/zsign/blob/v1.1.2/src/zsign.cpp#L479-L498).

`-m` can repeat for extension profiles. `-C` checks certificate/OCSP status; it is not a complete Apple archive validator. The documented `-p` option puts a password on argv. Orchard should instead use a short-lived protected PEM file, or upstream a file-descriptor/password-file interface before supporting encrypted P12 input. Do not silently use ad-hoc signatures or development profiles for an App Store build. [zsign source and CLI](https://github.com/zhlynn/zsign/tree/v1.1.2).

Before signing, bind the exact bundle ID, team ID, profile UUID, certificate fingerprint and artifact hash. Verify the certificate private-key match, expiry, profile certificate membership, distribution profile type, and entitlement compatibility. The signing entitlements must reflect the profile's application identifier/team prefix and the app's approved capabilities. App Store signatures must not enable `get-task-allow`; reject development devices/profiles for this route. Extensions need their own identifiers and profiles. Capabilities that require Apple's separate approval cannot be granted by Orchard.

## App bundle preparation and upload

The stable xtool packer supplies basic bundle values but does not establish a complete App Store archive. Its [open distribution issue](https://github.com/xtool-org/xtool/issues/117) reports missing `DTPlatformName`, rejected SDK provenance and a later successful workaround using Xcode. This is evidence of integration work still needed, not proof of native-Linux upload success.

Orchard should record actual SDK metadata while importing the XIP, then derive values from that selected SDK and toolchain rather than inventing Xcode versions. Inspect `DTPlatformName`, `DTPlatformVersion`, `DTPlatformBuild`, `DTSDKName`, `DTSDKBuild`, `DTXcode`, `DTXcodeBuild`, Mach-O `LC_BUILD_VERSION` minimum/SDK values and linked frameworks. App bundle validation should also check exact `CFBundleIdentifier`, version/build numbers, executable, device families, icon catalog, privacy manifests/descriptions, and extension metadata. Missing truthful provenance is a diagnostic, not permission to spoof metadata.

Apple currently says that uploads since **2026-04-28** must use **Xcode 26 or later and iOS/iPadOS 26 SDK or later**. That is Apple's stated requirement. A cross-compiler using extracted SDK contents is not proven equivalent merely by populating `DTXcode`. [Current upload requirements](https://developer.apple.com/news/upcoming-requirements/).

ASC's native upload sequence is create build upload, reserve build upload file, HTTP upload the byte ranges Apple returns, commit the file, then verify processing. [Apple build-upload resource](https://developer.apple.com/documentation/appstoreconnectapi/build-uploads), [Apple create operation](https://developer.apple.com/documentation/appstoreconnectapi/post-v1-builduploads), [ASC preparation code](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/internal/cli/shared/build_uploads.go), [Go HTTP uploader](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/internal/asc/upload.go).

```sh
asc builds upload --app APP_ID --ipa dist/Hello.ipa --wait --output json
asc builds info --build-id BUILD_ID --output json
asc validate --app APP_ID --version-id VERSION_ID --platform IOS --output json
asc review submit --app APP_ID --version-id VERSION_ID \
  --build-id BUILD_ID --platform IOS --dry-run --output json
```

After the user approves the concrete candidate:

```sh
asc review submit --app APP_ID --version-id VERSION_ID \
  --build-id BUILD_ID --platform IOS --confirm --output json
```

The combined upstream flow is `asc publish appstore --app APP_ID --ipa dist/Hello.ipa --version 1.0.0 --submit --confirm`. Orchard should normally keep upload, validation and submission separate so the approved binary is identifiable and retries cannot accidentally create another release.

In ASC 5, **`asc submit create` was removed**. The supported command is `asc review submit`. It requires `--app`, `--build-id`, exactly one of `--version` or `--version-id`, and `--confirm` unless `--dry-run`. `--build` is not a valid alias. `asc validate` also takes exactly one of `--version` or `--version-id`; `--strict` treats warnings as errors. These are remote App Store version checks, not local IPA validation. [Submit source](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/internal/cli/reviews/review_submit.go), [validation source](https://github.com/rorkai/App-Store-Connect-CLI/blob/5.0.0/internal/cli/validate/validate.go).

Keep Apple's upload URL capabilities and API tokens out of logs. ASC telemetry is enabled by default in current source; Orchard can set `ASC_TELEMETRY_DISABLED=1` and `DO_NOT_TRACK=1` for its managed subprocesses. API-key auth and Apple web-session auth are separate. Account enrollment, agreements, App Privacy publication and some declarations require an explicit guided browser step or a separately supported web adapter. Source code for an API client is not proof those prerequisites are satisfied for a user's account.

## Physical iPhone and iPad testing

`usbmuxd` supplies Linux USB device transport. libimobiledevice can provide pairing, device information, installation, logs and diagnostics. pymobiledevice3 has the more complete current CoreDevice workflow and is the recommended primary device adapter. It is GPL-3.0; retain its license and use it as a separately installed subprocess dependency. [libimobiledevice](https://github.com/libimobiledevice/libimobiledevice), [pymobiledevice3 installation](https://github.com/doronz88/pymobiledevice3/blob/v11.10.1/docs/installation.md).

```sh
pymobiledevice3 usbmux list
pymobiledevice3 apps list
pymobiledevice3 apps install dist/Hello-device.ipa
pymobiledevice3 mounter auto-mount
pymobiledevice3 developer dvt launch com.example.Hello
pymobiledevice3 developer dvt screenshot evidence/screen.png
pymobiledevice3 syslog live
```

The app must carry a signature/profile suitable for this device. An App Store distribution IPA does not become locally installable merely because its signature is valid. Pairing/trust and Developer Mode require the user's interaction on the physical device.

For modern iOS developer services, pymobiledevice3 uses RemoteXPC/RSD. Its current guide says iOS 17.4+ on Linux defaults to a process-local userspace tunnel without root. iOS 17.0 through 17.3.1 defaults to privileged `tunneld`. External LLDB connections need a reachable tunnel or supported local-port forwarding. Do not require root for every modern-device action or expose an unauthenticated tunnel listener on the network. [Current tunnel matrix](https://github.com/doronz88/pymobiledevice3/blob/v11.10.1/docs/guides/ios17-tunnels.md).

Current source also documents physical-device HID and display streams:

```sh
pymobiledevice3 developer core-device screen-capture screenshot evidence/screen.png
pymobiledevice3 developer core-device universal-hid-service tap -- 32768 32768
pymobiledevice3 developer core-device display start-video-stream evidence/capture.rtp --duration 10
```

The raw stream is length-prefixed RTP/HEVC, not an MP4 recording. The upstream `misc/rtp_dump.py` converts it to H.265. Expose recording only after that conversion and a playback check succeed. Coordinate gestures are not semantic accessibility assertions. Keep physical device testing evidence separate from unit tests, simulated tool transcripts and web dashboard screenshots. [CLI recipes](https://github.com/doronz88/pymobiledevice3/blob/v11.10.1/docs/guides/cli-recipes.md).

## SDK license boundary

Apple's currently published [Xcode and Apple SDKs Agreement](https://www.apple.com/legal/sla/docs/xcode.pdf) defines the SDKs as Apple Software. Its opening statement restricts execution to an Apple-branded product running macOS. Section 2.2.A limits installation to Apple-branded computers; section 2.5 prohibits separate SDK use and running parts on non-Apple hardware; section 2.7 restricts use and redistribution. Those are source terms, not an Orchard legal opinion about enforceability, exceptions or an individual user's additional agreements.

Consequently, manually downloading Xcode and accepting its license does not by itself establish permission for the Linux route. Keep Apple's files out of public packages, provide the original terms in setup, and obtain appropriate licensing advice or permission before marketing a native SDK workflow as licensed. This prerequisite does not change the technical product scope: Orchard must not add a macOS build service as a workaround. The entire build implementation remains on Linux.

## Recommended implementation order

1. Build a durable operation model shared by CLI and app, including dry-run plans, JSON results, streamed redacted events, cancellation and artifact hashes. An AI agent uses the same commands as a person.
2. Install checksum-pinned open-source tools in user-owned directories, diagnose host/device prerequisites, and import user-provided SDK metadata with clear provenance.
3. Generate the SwiftPM app; build through xtool; compile icons with the AssetKit executable; merge verified build metadata before signing.
4. Implement CSR/certificate/profile issuance and local identity checks, then zsign for separate development and distribution artifacts.
5. Install the device artifact, launch it, retain screenshots/logs/recording when supported, and collect explicit user test observations.
6. Upload the distribution artifact with ASC, retain exact Apple build/upload IDs and processing state, apply metadata, validate the App Store version, then request the final release approval.
7. Verify the full route on Linux amd64 and arm64 with real iPhone and iPad hardware and one developer-owned app. Until that evidence exists, label native store delivery as an integration under validation.
8. Add broader Linux resource compiler support, reproducible Linux packaging and additional framework adapters without implying those paths already work. Hosted macOS is excluded.
