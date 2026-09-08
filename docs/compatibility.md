# Compatibility and proof matrix

Pomeforge 0.1.0 targets Linux amd64 and arm64 with `.deb`, `.rpm`, Arch packages and portable archives. The prebuilt helpers target glibc 2.35 or newer and the GCC 12 C++ runtime (`GLIBCXX_3.4.30`). Packages include the CLI, asset compiler and unxip; first run downloads the pinned Swift 6.3.3 Ubuntu 22.04 toolchain. Apple SDKs remain operator-supplied. A matching libc version alone does not establish full distribution compatibility.

## 0.1.0 release checks

Public source `f9c899039193fde07b60291e479a8ee8aa7f39a6` passed [both native package builds and all nine fresh distribution jobs](https://github.com/gerdums/pomeforge/actions/runs/34289230791). Each fresh job installed its native package, downloaded the pinned Swift runtime as a normal user, installed xtool, registered helpers, generated the SwiftUI starter and passed tool discovery. It also compiled and ran a separate native Swift Hello World executable. These jobs did not import an Apple SDK or compile iOS code.

The release supplies private, pinned ncurses/tinfo libraries for Swift; this resolves the missing legacy ncurses library on Arch and incompatible symbol-version behavior on Fedora. These files are installed inside the user runtime and retain their license notice. No system library replacement is required.

## Earlier 0.1.0 candidate evidence

The following checks used `33c8aad`, before the later runtime archive-containment correction. Exact hashes and source boundaries are in [verification.md](verification.md).

| Check | Confirmed scope |
| --- | --- |
| Package build | Native Linux arm64 produced all four package formats using Go 1.24.13 and Swift 6.3.3 on Ubuntu 22.04 |
| Desktop setup and workspace | Fresh `.deb` on Ubuntu 24.04, non-root GIO 2.80/xterm/Xvfb and Chromium: all four grouped checks passed; setup downloads were declined, the window closed on Enter, and a separate fresh workspace created HelloWorld through the real authenticated UI |
| Portable archive | Eight non-root Linux arm64 checks passed, including installation into a path with spaces, repeat installation, helper execution and both desktop entries |
| Complete guided local setup | Fresh Linux arm64 quickstart passed in 192.807 seconds, including the official runtime download, supplied full-XIP SDK import, six metadata-source hash checks and HelloWorld compilation; no Apple account/device operation |
| Generated debug app | Actual iPhoneOS 26.5 SDK selected; arm64 executable produced. Its debug Mach-O stamps minimum iOS 17 / SDK 17, so this run does not prove release metadata or Store readiness |

The current native package and host Swift checks are recorded above. Native amd64 iOS compilation, native Omarchy desktop behavior, physical USB access and Apple outcomes remain separate acceptance gates.

## Distribution targets

| Target | Package and baseline | Evidence boundary |
| --- | --- | --- |
| Ubuntu 22.04/24.04; Debian 12/13 | `.deb`, amd64/arm64; glibc ≥2.35 and libstdc++ ≥12 | Fresh Ubuntu 22.04, Debian 12 and Debian 13 package/toolchain/native Swift smoke passed on amd64 and arm64; separate Ubuntu 24.04 container desktop evidence and Ubuntu 22.04 arm64 iOS quickstart evidence above |
| Fedora | `.rpm`, amd64/arm64; compatible glibc/C++ runtime and package dependencies | Fresh Fedora 44 package/toolchain/native Swift smoke passed on amd64 and arm64; host SELinux, desktop and USB behavior unproved |
| Arch / Omarchy | Arch package or portable archive; compatible runtime plus `libxml2-legacy` | Fresh native amd64 Arch package/toolchain/Swift smoke passed; native Omarchy, Arch arm64 and host USB/udev behavior unproved. Earlier QEMU results below are retained separately |
| Other glibc distributions | Portable amd64/arm64 archive with required distro libraries | Portable installation passed on Linux arm64; verify the target's shared-library versions, desktop opener and device services |
| Alpine / other musl distributions | A compatible local Linux container | Bundled Swift helpers target glibc; the static Go CLI alone does not establish a complete musl workflow |
| Other CPU architectures | No 0.1.0 binary asset | Upstream iOS toolchains may not supply compatible host binaries |

System library dependencies, host service access and device trust remain prerequisites. Setup does not silently invoke sudo or replace a system compiler. See the [local Linux container guide](container.md).

## Historical foundation evidence

The core and release results use product source at `4171935b3230da04bf35ba69ba042563ce00bae8`, which is identical to the independently reviewed product source at `bb831ed`. The later desktop correction was delivered at `e8e4027` and independently reviewed and tested at `c10774b`. Its fresh Linux build has the same CLI hash as the earlier proof. Source revisions, binary hashes and retained proof boundaries are recorded in [verification.md](verification.md).

| Component | Environment | Result |
| --- | --- | --- |
| CLI and core | Go 1.24.13, native Linux arm64 | 306 test/subtest pass events and one opt-in skip |
| Graphical workspace | Linux Chromium | Final graphical checks passed, including setup, project and release controls, safe error rendering, plan invalidation, authentication/Origin checks and narrow viewport; screenshots and a 7.04-second video retained |
| Apple SDK import | Swift 6.3.3, Linux arm64; operator-supplied Xcode 26.6 XIP | Fresh full-XIP import passed in 122.561 seconds using default unxip extraction; six original metadata files, nine values and the selected Swift SDK configuration were verified |
| SwiftUI release build | Same Linux arm64 environment; actual iPhoneOS 26.5 SDK | Generated project compiled in 40.386 seconds; Mach-O records minimum iOS 17.0 and SDK 26.5, with matching SDK-derived release metadata |
| Assets and IPA export | Same Linux arm64 environment; real AssetKit and zsign 1.1.2, synthetic identity and explicit fixture trust root | Full native run passed 27 checks, including icon generation, release build, export and IPA inspection; 18 icon slots produced 15 PNG files plus `Assets.car`, and the unsigned source bundle was preserved |
| Go 1.24.13 amd64 executable | Arch Linux amd64 under QEMU | Project generation crashed during PNG encoding; the failure is retained; current native amd64 project generation and host Swift checks passed separately above |
| Separate Go 1.27.1 amd64 executable | Same emulated Arch environment; binary hash beginning `cb2bb545` | Project generation and a correctly blocked build plan passed; iOS compilation was not exercised, and the Go 1.24.13 failure remains recorded |
| Installed desktop entry | Debian GIO 2.74.6 and Ubuntu GIO 2.80.0 | Independent review, 25 Node tests and container contracts passed; actual Ubuntu GIO launched the installed app from a special-character path and Chromium created a real project; screenshot and 2.12-second video retained |
| Physical iPhone/iPad | Pending Linux device access | Pairing, provisioning, installation, launch and recorded interaction remain unproved |
| TestFlight and App Store | Pending real Apple identity and approved account operations | Apple certificate trust, upload, successful processing, TestFlight and App Review submission remain unproved |

The exported IPA used a synthetic profile and identity with an explicitly supplied fixture root. A private PKCS#12 adapter carried that test CA into the unchanged native signer. The result establishes local build, asset processing, signing-tool execution and structural inspection. It does not establish Apple's trust or acceptance; Apple processing reports `not_checked`, and independent code-signature cryptographic verification reports `not_performed`. The XIP hash and retained metadata establish operator-supplied provenance; Apple archive-signature authentication was not performed.

The successful Go 1.27.1 result is limited to the recorded emulated Arch operations. A native Omarchy session, native amd64 iOS compilation, host USB permissions and distribution-specific desktop behavior still need direct verification.

### Earlier upstream and adapter evidence

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
