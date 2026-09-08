# Implementation plan

Pomeforge keeps every build step on Linux. The graphical workspace and JSON CLI share one implementation, and AI is optional. The current scope is a Swift Package Manager app with SwiftUI, one iPhone/iPad app target, and a physical-device workflow.

## Delivered foundation

The repository contains project generation, a typed manifest, dependency diagnosis, inspectable operation plans, explicit execution, private result records, a graphical workspace, and a desktop launcher. The CLI exposes its commands, action parameters, effects, errors, and confirmation requirements as JSON for agents.

Onboarding installs checksum-pinned xtool, ASC, and zsign in user-owned state. It registers source-built helpers and imports an operator-supplied Xcode archive entirely on Linux using unxip, xtool, and native Swift SDK installation. The receipt preserves the original SDK metadata and binds release builds to the selected device SDK.

The release path includes icons, an unsigned release build, private named identities, profile and entitlement checks, AssetKit compilation, zsign export, IPA inspection, and separate ASC upload, status, validation, and review-submission actions. Portable Linux binaries and a Linux container package the same implementation. Exact execution evidence and remaining limits are recorded in [verification.md](verification.md).

## Next acceptance gates

1. **Physical iPhone and iPad.** On a Linux machine with USB access, pair and trust each device, enable Developer Mode, then build, provision, install, and launch the generated app. Retain screenshots and video of meaningful interaction. Exercise locked devices, missing profiles, multi-device selection, and connection loss. A simulator or a successful build does not satisfy this gate.
2. **Apple distribution.** With the owner's selected app and authorized account, create or import a real distribution identity, export the exact IPA, upload it, and retain successful Apple processing and TestFlight installation. Finish store metadata, privacy declarations, screenshots, and export compliance before an explicitly approved review submission. Production publishing, signing-policy changes, and submission remain owner-controlled.
3. **Native Omarchy and other distributions.** Test the complete toolchain on native Omarchy/Arch x86_64, then Ubuntu/Debian and Fedora on amd64 and arm64. Verify Swift libraries, managed AppImage extraction, desktop launching, USB permissions, and device services. Emulated Arch CLI tests do not establish an Omarchy desktop result. For musl distributions, verify the documented compatible Linux-container path before claiming the whole toolchain works.
4. **Broader project support.** Add frameworks, extensions, Watch targets, project import, Flutter, or React Native only with a separate complete Linux build/signing adapter and retained end-to-end evidence. Interface Builder and simulator support need their own implementation; no remote Mac fallback is planned.
5. **Developer experience.** Use real project feedback to prioritize editor/LSP integration, a packaged native window, distro packages, and richer device diagnostics. Add MCP only if the existing noninteractive JSON CLI leaves a concrete agent workflow gap.

These gates define further verification and scope. Local synthetic signing fixtures do not establish Apple's trust or App Store acceptance.
