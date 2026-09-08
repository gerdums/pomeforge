# Compatibility and proof matrix

Orchard targets Linux distributions through portable amd64 and arm64 binaries. Dependencies have their own requirements. A portable launcher does not prove that an iOS compiler, signing tool, or device service works on every distribution.

## Current evidence

| Component | Environment | Result |
| --- | --- | --- |
| xtool 1.19.0 | Debian 12, Linux arm64 container | Published AppImage hash verified; extraction and CLI help/version execute without FUSE |
| ASC 5.0.0 | Debian 12, Linux arm64 container | Published binary hash verified; version and command help execute without network or Apple credentials |
| zsign 1.1.2 | Debian 12, Linux arm64 container | Published archive hash verified; extracted signer version/help execute |
| AssetKit pinned source | Swift 6.3.3, Linux arm64 container | Upstream test runner reports 38 tests; external rsvg-convert test skipped, Apple assetutil gate unavailable on Linux |
| Orchard app and CLI | Linux arm64; Arch Linux amd64 under CPU emulation | Baseline binaries, tests and live graphical flow passed; final composed checks tracked in `verification.md` |
| iOS SDK and app cross-build | Swift 6.3.3 Linux arm64; operator-supplied Xcode 26.6 / iPhoneOS 26.5 SDK | Actual SDK extraction/install and SwiftUI cross-build passed; final release integration remains under verification |
| Physical iPhone/iPad | Pending Linux device access | Pairing, build/install/launch and media evidence required |
| TestFlight and App Store | Pending approved account/app/signing setup | Exact IPA hash and successful Apple processing required |

The upstream AssetKit revision under test is `e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7`. The official Swift Linux arm64 image is pinned to `sha256:f2c50c083788a59d534a26c5d90b54023494a1c7d875aa6ef8c5837b0e08c7aa`. This upstream test does not prove Orchard's eventual wrapper or Apple's acceptance of an exported app.

## Distribution targets

| Target | Packaging approach | Required follow-up |
| --- | --- | --- |
| Omarchy / Arch x86_64 | Portable executable, desktop entry, user-owned managed tools | Test a real Omarchy session, udev permissions, AppImage extraction and Swift dependencies |
| Ubuntu / Debian amd64 and arm64 | Portable executable and extracted managed tools | Test supported libc versions and USB service installation |
| Fedora amd64 and arm64 | Same executable; distro-specific prerequisite guidance | Validate SELinux/device permissions and Swift dependencies |
| Alpine / other musl distributions | Orchard core can be static; upstream tools differ | Do not mark the complete pipeline supported until Swift and xtool are tested in a compatible container |
| Other CPU architectures | Source build of Orchard where Go supports it | Upstream iOS toolchains may not supply compatible host binaries |

Orchard must diagnose missing Swift, usbmuxd, device trust and unsupported architectures separately. Setup should never silently invoke sudo or replace a system compiler.
