# Verification record

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

## Remaining evidence

Physical iPhone/iPad installation and interaction, a real Apple distribution identity, Apple upload processing, TestFlight installation and App Review submission have not been established. Native Omarchy desktop and native amd64 complete-toolchain testing also remain outstanding. Portable binary execution is a narrower result than whole-distribution compatibility.

The final Go 1.24.13 amd64 binary reported its version under emulation but crashed in the Go runtime (`lfstack.push`) while generating a project icon. The same source built on Linux with Go 1.27.1 passed project creation and generation of a correctly blocked build plan in fresh emulated Arch state. Both the failed Go 1.24.13 result and separate Go 1.27.1 binary provenance are retained; no emulator workaround or global configuration change was used. Native Go 1.24.13 Linux arm64 checks passed.

An earlier managed xtool installation on emulated Arch failed at the QEMU binfmt handler's AppImage marker matching; explicit QEMU extraction and the same extracted xtool binary succeeded. That failed environment has not been relabeled as passing. The complete managed installer passed on native Linux arm64. See the [compatibility matrix](compatibility.md) for distribution targets.

Source archives, actual app/IPA outputs, logs, screenshots, videos and hash manifests are retained privately outside the reusable repository. [Earlier Orchard verification](history/orchard-verification.md) preserves the original development records and their source revisions. The [implementation plan](implementation-plan.md) defines the remaining acceptance gates.
