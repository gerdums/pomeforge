# Compatibility and proof matrix

Pomeforge targets Linux distributions through portable amd64 and arm64 binaries. Dependencies have their own requirements. The evidence below identifies the environments and operations actually exercised; it does not establish the complete pipeline on every distribution.

## Current Pomeforge evidence

The core and release results use product source at `4171935b3230da04bf35ba69ba042563ce00bae8`, which is identical to the independently reviewed product source at `bb831ed`. The later desktop correction was delivered at `e8e4027` and independently reviewed and tested at `c10774b`. Its fresh Linux build has the same CLI hash as the earlier proof. Source revisions, binary hashes and retained proof boundaries are recorded in [verification.md](verification.md).

| Component | Environment | Result |
| --- | --- | --- |
| CLI and core | Go 1.24.13, native Linux arm64 | 306 test/subtest pass events and one opt-in skip |
| Graphical workspace | Linux Chromium | Final graphical checks passed, including setup, project and release controls, safe error rendering, plan invalidation, authentication/Origin checks and narrow viewport; screenshots and a 7.04-second video retained |
| Apple SDK import | Swift 6.3.3, Linux arm64; operator-supplied Xcode 26.6 XIP | Fresh full-XIP import passed in 122.561 seconds using default unxip extraction; six original metadata files, nine values and the selected Swift SDK configuration were verified |
| SwiftUI release build | Same Linux arm64 environment; actual iPhoneOS 26.5 SDK | Generated project compiled in 40.386 seconds; Mach-O records minimum iOS 17.0 and SDK 26.5, with matching SDK-derived release metadata |
| Assets and IPA export | Same Linux arm64 environment; real AssetKit and zsign 1.1.2, synthetic identity and explicit fixture trust root | Full native run passed 27 checks, including icon generation, release build, export and IPA inspection; 18 icon slots produced 15 PNG files plus `Assets.car`, and the unsigned source bundle was preserved |
| Go 1.24.13 amd64 executable | Arch Linux amd64 under QEMU | Project generation crashed during PNG encoding; the failure is retained and native amd64 behavior remains unproved |
| Separate Go 1.27.1 amd64 executable | Same emulated Arch environment; binary hash beginning `cb2bb545` | Project generation and a correctly blocked build plan passed; iOS compilation was not exercised, and the Go 1.24.13 failure remains recorded |
| Installed desktop entry | Debian GIO 2.74.6 and Ubuntu GIO 2.80.0 | Independent review, 25 Node tests and container contracts passed; actual Ubuntu GIO launched the installed app from a special-character path and Chromium created a real project; screenshot and 2.12-second video retained |
| Physical iPhone/iPad | Pending Linux device access | Pairing, provisioning, installation, launch and recorded interaction remain unproved |
| TestFlight and App Store | Pending real Apple identity and approved account operations | Apple certificate trust, upload, successful processing, TestFlight and App Review submission remain unproved |

The exported IPA used a synthetic profile and identity with an explicitly supplied fixture root. A private PKCS#12 adapter carried that test CA into the unchanged native signer. The result establishes local build, asset processing, signing-tool execution and structural inspection. It does not establish Apple's trust or acceptance; Apple processing reports `not_checked`, and independent code-signature cryptographic verification reports `not_performed`. The XIP hash and retained metadata establish operator-supplied provenance; Apple archive-signature authentication was not performed.

The successful Go 1.27.1 result is limited to the recorded emulated Arch operations. A native Omarchy session, native amd64 iOS compilation, host USB permissions and distribution-specific desktop behavior still need direct verification.

## Earlier upstream and adapter evidence

These checks predate the final Pomeforge composition. They remain useful dependency evidence with their original scope.

| Component | Environment | Result |
| --- | --- | --- |
| xtool 1.19.0 | Debian 12, Linux arm64 container | Published AppImage hash verified; extraction and CLI help/version execute without FUSE |
| ASC 5.0.0 | Debian 12, Linux arm64 container | Published binary hash verified; version and command help execute without network or Apple credentials |
| zsign 1.1.2 | Debian 12, Linux arm64 container | Published archive hash verified; extracted signer version/help execute |
| Managed ASC and zsign | Fresh non-root Arch Linux amd64 under QEMU | Real pinned installation, discovery and version checks passed on provisional setup source |
| xtool 1.19.0 amd64 | Same emulated Arch environment | Pinned download verified; managed AppImage execution blocked by binfmt marker matching, while explicit QEMU extraction and extracted xtool execution passed |
| AssetKit pinned source | Swift 6.3.3, Linux arm64 container | Upstream test runner reports 38 tests; external rsvg-convert test skipped, Apple assetutil gate unavailable on Linux |

The AssetKit revision is `e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7`. The official Swift Linux arm64 image used for native proof is pinned to `sha256:f2c50c083788a59d534a26c5d90b54023494a1c7d875aa6ef8c5837b0e08c7aa`. Pomeforge's wrapper and final export path were exercised separately in the native run above. Historical command and artifact records are retained in [the earlier verification record](history/orchard-verification.md).

## Distribution targets

| Target | Packaging approach | Required follow-up |
| --- | --- | --- |
| Omarchy / Arch x86_64 | Portable executable, desktop entry, user-owned managed tools | Test a native Omarchy session and amd64 iOS build, udev permissions, AppImage extraction and Swift dependencies; retain the emulation limitations above |
| Ubuntu / Debian amd64 and arm64 | Portable executable and extracted managed tools | Extend the native arm64 container evidence to supported host libc versions, desktop launch and USB service installation |
| Fedora amd64 and arm64 | Same executable; distro-specific prerequisite guidance | Validate SELinux/device permissions and Swift dependencies |
| Alpine / other musl distributions | Static Pomeforge core and a local compatible Linux container for upstream tools | Verify the full container workflow and any required device access on the target host |
| Other CPU architectures | Source build of Pomeforge where Go supports it | Upstream iOS toolchains may not supply compatible host binaries |

Swift availability, usbmuxd access, device trust and CPU architecture remain separate prerequisites. Setup does not silently invoke sudo or replace a system compiler. See the [local Linux container guide](container.md) for the compatibility fallback and its host requirements.
