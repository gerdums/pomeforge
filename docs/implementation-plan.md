# Implementation plan

## 1. Runnable foundation

Create the Go CLI, typed manifest, SwiftUI phone/tablet template, dependency doctor, command planner, guarded subprocess executor, JSON interface, and private result records. Build a responsive local graphical workspace and Linux desktop launcher. Tests cover path validation, stale plans, refusal of unconfirmed external writes, failed subprocesses, credential redaction, and loopback API protection.

## 2. Toolchain onboarding

Validate current xtool and ASC releases. Add verified dependency acquisition where upstream checksums are available, explicit Swift/SDK setup handoff, USB diagnostics, tool version locking, and repair instructions. Exercise Omarchy/Arch and Ubuntu on both supported CPU architectures; treat other distributions as compatibility targets until tested.

## 3. Native Linux build and device loop

Compile the included SwiftUI app with an SDK obtained on Linux. Install and launch on a physical iPhone and iPad, retain build output and device screenshots/video, and verify offline build reuse. Test failed pairing, locked devices, Developer Mode, missing profiles, multi-device selection, and connection loss. Do not substitute an Xcode build or simulator for this proof.

## 4. Distribution

Create or import a distribution identity on Linux, obtain matching profiles, export and inspect the IPA, validate metadata, upload with ASC, wait for processing, and deliver to TestFlight. Then exercise review submission using an explicitly approved app and account. Check entitlements, icons/assets, SDK minimums, privacy manifests, screenshots, export compliance, and review metadata. Publication and signing-policy changes require the owner's approval.

## 5. Broader compatibility

Add a packaged native window, distro packages, editor/LSP integration, device diagnostics, project import, and additional framework adapters only as their complete Linux toolchains are verified. Every build stage remains on Linux; hosted macOS is excluded. Keep unsupported paths visible. Add a stable MCP adapter if JSON CLI usage reveals a concrete need; the CLI already supports coding agents without vendor coupling.

## Completion evidence

The initial delivery includes implemented behavior and exact checks in `docs/verification.md`. This plan is not a claim that the physical-device or App Store gates have passed.
