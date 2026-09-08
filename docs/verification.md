# Verification record

## Published 0.1.0 source and Linux checks

The release packages were built from public source `f9c899039193fde07b60291e479a8ee8aa7f39a6`. Subsequent documentation updates record the results without changing those artifacts. All compilation, packaging, installation and runtime checks below ran in Linux.

[Release build 34289230791](https://github.com/gerdums/pomeforge/actions/runs/34289230791) passed both native package builds and all nine fresh installation jobs. [CI 34289220074](https://github.com/gerdums/pomeforge/actions/runs/34289220074) passed Go tests/vet, web and packaging tests, and the Swift asset-compiler checks.

| Check | Environment | Result |
| --- | --- | --- |
| Native release packages | Native Linux amd64 and arm64; Go 1.24.13 and Swift 6.3.3 on Ubuntu 22.04 | All eight `.deb`, `.rpm`, Arch and portable packages built |
| Fresh native package and toolchain setup | Ubuntu 22.04, Debian 12, Debian 13 and Fedora 44 on amd64 and arm64; Arch on amd64 | All nine passed: dependencies installed, normal-user pinned Swift and xtool installation, helper discovery, HelloWorld generation and doctor checks |
| Swift compiler and package manager | Each of the same nine fresh environments | Parsed the generated SwiftUI project's package manifest, then compiled and ran a separate native Swift Hello World executable; these checks do not compile iOS code |
| Runtime and portable installer regression checks | Linux; 21 focused tests | Passed archive containment, existing-install preservation, compatibility-library integrity and license retention checks |
| Downloaded release-file inspection | All eight final GitHub-built packages, inspected on Linux | Checksums, native metadata, all 40 bundle-manifest entries, ELF architecture, cross-format file identity, 19 license files, corresponding unxip source and private compatibility-library pins/ABI passed |
| Final packaged quickstart resume | Final ARM64 `.deb`, normal-user Ubuntu 22.04 terminal, existing SDK/runtime/project | Passed in 8.057 seconds and rebuilt the SwiftUI starter; all 23 project source files, bundle identifier, SDK metadata and runtime receipt preserved. This reused the pre-compatibility runtime; fresh runtime installation is established separately by the nine distro jobs |
| Final installed desktop flows | Exact final ARM64 `.deb`, fresh normal-user Ubuntu 24.04 desktop state, GIO/xterm/Chromium | Passed complete installed-payload verification, actual Setup consent/download-decline/terminal-close flow, Workspace menu launch and authenticated HelloWorld creation; four screenshots and 3.75-second setup / 1.88-second workspace videos retained |

The final compatibility change also passed independent review with no findings. Its reviewed private revision `66217f5109263d5317648f58cf5bc97d3fc0816b` matches all 147 product paths, contents and executable modes at the public release source; documentation and private orchestration history are separate.

The final ARM64 `.deb` SHA-256 is `247ff08676f62283e8cbef9f456eac0561018c681a4fc58f40ca6b329ecced1d`; the amd64 `.deb` is `322080bb670e0e3167cf33fd950020d30fb052f6ed94a6aaa5f1c34a5810c314`. The release's `SHA256SUMS` covers all eight packages and both provenance files.

The runtime includes private, version-pinned ncurses/tinfo libraries to support the official Swift toolchain on Fedora and Arch. Their upstream Ubuntu binary-package pins, resulting hashes, architecture and ABI checks are recorded in the release provenance. The libraries are bundled with Pomeforge and copied into new user runtimes; the system compiler and libraries are not replaced.

## Earlier 0.1.0 candidate evidence

These checks used candidate `33c8aadfe222c6b29501162eaf77345829068d7f`, before the later runtime archive-containment correction. They establish the recorded candidate behavior, not a later artifact by implication. All builds, installation, terminal interaction and browser automation ran in Linux.

| Check | Environment | Result |
| --- | --- | --- |
| Native release packaging | Linux arm64, Go 1.24.13, Swift 6.3.3 Ubuntu 22.04 image | `.deb`, `.rpm`, Arch package and portable archive built; asset compiler and unxip are prebuilt with static Swift libraries |
| Installed desktop flows | Fresh `.deb`, non-root Ubuntu 24.04 container, GIO 2.80, xterm/Xvfb and Chromium | Four grouped checks passed: exact package/CLI identity, setup consent and download decline, fresh workspace launch, authenticated HelloWorld creation with on-disk Swift source; screenshots and 3.92-second setup / 1.92-second workspace videos retained |
| Portable installation | Actual portable archive, non-root Linux arm64 | Eight checks passed, including manifest verification, a path with spaces, repeat installation, CLI/helper execution, both menu entries and structured rejection of unattended quickstart; installation created no runtime or workspace |
| Fresh packaged quickstart | Non-root Linux arm64, empty runtime/SDK state, operator-supplied Xcode 26.6 archive | Passed with exit 0 in 192.807 seconds: Linux tools ready at 55.485 seconds, SDK import and validation reached the build at 154.517 seconds, then HelloWorld compiled in about 38.29 seconds |
| SDK and app inspection | Same quickstart run, actual iPhoneOS 26.5 SDK | Six retained metadata-source hashes, private `0600` receipt and active `arm64-apple-ios` selection verified; output is an arm64 iOS Mach-O executable. This debug build records minimum iOS 17 and linked SDK 17; it is not release-metadata or Store-readiness proof |

The tested `.deb` SHA-256 is `a1c9029dbb08cd66dec942c12869c77eaf2ea6bc270ccb7516354e6195471128`; its installed CLI is `3a6640cf2a210df148d5cd2d32f66fd4d338ca199c22e85ff099cdc77a0c230e`. The portable archive SHA-256 is `7aa4a7ca3f7c38bce7757902b31b38646fbf98177de0bce74a0f4e786fd392e4`. The setup decline and full quickstart are separate runs; neither performed Apple account or physical-device operations.

## Current coverage and outstanding checks

The fresh distribution checks above establish native package installation and host Swift compilation. They do not establish iOS compilation, a graphical host session or USB access on each distribution.

Native amd64 iOS compilation remains a separate gap. Native Omarchy desktop behavior, host USB/udev permissions, Fedora SELinux behavior, physical iPhone/iPad installation and interaction, a real Apple distribution identity, upload processing, TestFlight and App Review still need direct proof. Container checks do not establish these host or Apple outcomes.

## Historical foundation evidence

The native SDK/release and container checks ran against Pomeforge source `4171935b3230da04bf35ba69ba042563ce00bae8`. Graphical and core QA ran against the independently reviewed revision `bb831ed60c2dad569e993ddf64077d78cf2c6f6e`; all 121 product paths, file contents and executable modes match that delivery revision exactly. The revisions differ in private orchestration packets and Git ancestry. A later desktop-launcher compatibility correction was tested at `c10774b3f8043dc641530795b38b84af3c2c0957`, corresponding to delivered source `e8e402795f75ddd3098ae0d3dace37d46652b9e0`. It changes installer escaping and its regression coverage; a fresh Linux build produces the same CLI hash as the earlier core, graphical and iOS proof. Subsequent documentation-only updates record these results. All compilation, SDK extraction, asset processing, signing, tests, and browser automation below ran in Linux. The workstation only orchestrated containers, edited source, and collected evidence.

| Check | Environment | Result |
| --- | --- | --- |
| Core and CLI | Go 1.24.13, Linux arm64 | 306 test/subtest pass events with the race detector; one opt-in live-download integration test skipped; vet and static Linux arm64/amd64 builds passed |
| Arch executable | Same source built with Go 1.27.1 on Linux arm64; non-root pinned Arch Linux amd64 under CPU emulation | Actual version, project creation and generation of a correctly blocked build plan passed |
| Desktop and container contracts | Non-root Linux with Node and GIO | 25 Node tests and container contracts passed; real GIO launch passed with Debian GIO 2.74.6 and Ubuntu GIO 2.80.0 |
| Fresh graphical setup | Non-root Linux arm64 and Chromium | Real pinned ASC 5.0.0 download, SHA-256, discovery/version and visible setup success passed before a project existed |
| Graphical release controls | Same fresh session, explicitly synthetic identity | Private signing-path redaction, identity selection, truthful missing-bundle error, disabled execution, stale-plan invalidation, authentication/Origin rejection, narrow viewport and no page errors passed; screenshots and 7.04-second video retained |
| Installed desktop launch | Linux, actual GIO and Chromium, special-character install path | Actual GIO launched the installed CLI, handed its private URL to Chromium, created a real project and displayed the correct blocked build plan; all six flow checks passed; 2.12-second video retained |
| Asset compiler | Pinned Swift 6.3.3 Linux arm64 | All 11 wrapper tests and release build passed; source files match the final product |
| Fresh full-XIP SDK import | Operator-supplied Xcode 26.6 archive; unxip 3.3.0, xtool 1.19.0, Swift 6.3.3 Linux arm64 | Passed from empty state in 122.561 seconds; all six original metadata files and nine values independently verified, with active Swift SDK selection checked |
| Generated iPhone/iPad app | Real iPhoneOS 26.5 SDK, Linux arm64 | Generated SwiftUI release app compiled in 40.386 seconds using the selected real SDK |
| Native export and inspection | Real AssetKit and zsign 1.1.2; synthetic signing fixture | 27 checks passed across the fresh SDK and release workflow; 15 icon PNGs and Assets.car present, exact embedded profile and Mach-O/DT records checked, source bundle preserved |
| Container image | Exact product source, Linux arm64 | Build, default UID 10001, private persistent state, helper registration and real pinned xtool/ASC/zsign installs passed |

The Linux arm64 CLI SHA-256 is `a9ba4728b28cccbca9c04e4014d3ffd1cf3bf0c2351fd4b169333b1308c771c9`; the source archive SHA-256 is `ec0cd268b5522278794fceea3dd01b6a158ed7ac5dea1124de7d92e47c71e4ca`.

The final native fixture IPA SHA-256 is `2c0eaca08f1a40cdb42f85d31718f41b766bbfd45797750c9fb3e7549a9e6157`. The Mach-O records minimum iOS 17 and SDK 26.5; its DT metadata comes from the retained SDK records. The source bundle and catalog remained unchanged during export. Inspection, archive checks and the receipt establish only the facts they report.

The synthetic identity uses an arbitrary test CA through a private PKCS#12 fixture adapter to native zsign. This does not prove a real Apple distribution identity, Apple's trust, cryptographic code-signature verification, device installation or Apple processing. The receipt reports `appleProcessing: "not_checked"` and code-signature cryptographic verification as `not_performed`. This fixture IPA is not offered as an installable or App Store-ready app.

The supplied Xcode archive SHA-256 is `06384762f286fb4d20440f7a67ba40cdf79bedb1cbee9e5111e3030a53a6cb81`. It reports Xcode 26.6/build 17F113, SDK version 26.5 and SDK system build 23F81a. Its provenance is operator-supplied; Pomeforge does not claim to have verified Apple's archive signature. No Apple SDK is included in the repository or distributable container image.

### Historical limitations and emulation failures

At this earlier checkpoint, physical-device and Apple account outcomes, native Omarchy desktop behavior and native amd64 complete-toolchain testing were unproved. The later package evidence above has its own narrower scope.

The final Go 1.24.13 amd64 binary reported its version under emulation but crashed in the Go runtime (`lfstack.push`) while generating a project icon. The same source built on Linux with Go 1.27.1 passed project creation and generation of a correctly blocked build plan in fresh emulated Arch state. Both the failed Go 1.24.13 result and separate Go 1.27.1 binary provenance are retained; no emulator workaround or global configuration change was used. Native Go 1.24.13 Linux arm64 checks passed.

An earlier managed xtool installation on emulated Arch failed at the QEMU binfmt handler's AppImage marker matching; explicit QEMU extraction and the same extracted xtool binary succeeded. That failed environment has not been relabeled as passing. The complete managed installer passed on native Linux arm64. See the [compatibility matrix](compatibility.md) for distribution targets.

Source archives, actual app/IPA outputs, logs, screenshots, videos and hash manifests are retained privately outside the reusable repository. [Earlier Orchard verification](history/orchard-verification.md) preserves the original development records and their source revisions. The [implementation plan](implementation-plan.md) defines the remaining acceptance gates.
