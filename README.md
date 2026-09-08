# Orchard

Build Swift iPhone and iPad apps from Linux, using a graphical workspace or an agent-friendly CLI. AI is optional. Every compilation, asset-processing, signing, and export step runs on Linux. There is no remote Mac backend.

Orchard combines [xtool](https://github.com/xtool-org/xtool), [AssetKit](https://github.com/xtool-org/AssetKit), [zsign](https://github.com/zhlynn/zsign), and [App Store Connect CLI](https://github.com/rorkai/App-Store-Connect-CLI) with project setup, dependency installation, inspectable plans, validation, and operation records.

This repository is an early implementation. Read the [verification record](docs/verification.md) for the tested revision and the distinction between working local tooling, a real iOS build, device execution, and Apple processing. The initial project format is a Swift package with a SwiftUI application. Arbitrary Xcode projects, Flutter, React Native, Interface Builder, and an iOS simulator are separate future adapters.

## Start on Linux

Build Orchard with Go 1.24 or newer on a Linux host:

```sh
make check
./bin/orchard app --workspace "$HOME/Orchard" --listen 127.0.0.1:0 --open
```

The workspace opens in your browser. `--open` uses `xdg-open`; otherwise copy the local session URL printed by Orchard. Keep that URL private. The desktop launcher can be installed without root:

```sh
./packaging/install.sh ./bin/orchard
```

The [graphical workspace guide](docs/app.md) covers its controls and desktop installation. It runs on the same core as the CLI and needs no cloud account or language model.

## Create and work on an app

```sh
./bin/orchard init Fieldnotes --dir ./Fieldnotes --bundle-id com.example.fieldnotes
./bin/orchard doctor
./bin/orchard plan build --project ./Fieldnotes
./bin/orchard run build --project ./Fieldnotes --execute
```

Edit the generated Swift files with your preferred Linux editor. Orchard's workspace creates projects, diagnoses prerequisites, reviews operations, and displays their actual results; it is not a source editor or an iPhone emulator. Follow the [Linux device guide](docs/devices.md) to test with a physical iPhone or iPad.

Native iOS compilation needs Swift 6.3 or newer and an Apple SDK obtained by the operator. Orchard supplies no Apple SDK and does not fabricate SDK provenance. Physical devices require trust, Developer Mode, compatible provisioning, and Linux USB access. Distribution requires an eligible Apple Developer account and an appropriate signing identity/profile. Apple's SDK terms and current upload requirements are linked in the [toolchain research](docs/toolchain-research.md).

Read the [CLI guide](docs/cli.md) for the current command surface, JSON envelopes, execution controls and exit status. Dependency setup and native distribution commands are documented with their implementing packages while integration is being verified.

## Agent use

```sh
orchard schema --json
orchard doctor --json
orchard plan build --project ./Fieldnotes --json
orchard run build --project ./Fieldnotes --execute --json
```

Plans expose the steps and prerequisites before execution. Account writes additionally require explicit confirmation. The authenticated local API accepts known operations, not arbitrary commands. Failed operations retain their actual status and redacted output. Upload, validation, and review submission are separate actions; upload never automatically submits an app.

## Design and evidence

- [Architecture](docs/architecture.md) and [implementation plan](docs/implementation-plan.md)
- [Linux compatibility matrix](docs/compatibility.md)
- [Toolchain research](docs/toolchain-research.md) and [captured Linux command help](docs/upstream-command-reference.md)
- [Pinned dependency installer](internal/bootstrap/README.md) and [AssetKit bridge](tools/asset-compiler/README.md)
- [Third-party notices](THIRD_PARTY_NOTICES.md)

Omarchy/Arch is a primary target. Portable amd64 and arm64 binaries are the foundation for other distributions; compiler libraries, device services and desktop behavior still need distribution-specific verification.
