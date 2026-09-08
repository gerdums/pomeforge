# Pomeforge

Build Swift iPhone and iPad apps on Linux. Pomeforge provides a desktop workspace and CLI, with the same commands available to people and AI assistants. Every build step runs on Linux.

## Install 0.1.0

Download the package for your Linux distribution. Most PCs use **x86_64 / amd64**. Use **ARM64 / aarch64** for an ARM Linux computer.

| Linux distribution | x86_64 download | ARM64 download |
| --- | --- | --- |
| Ubuntu 22.04+, Debian 12+, Linux Mint 21+ | [Debian package](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge_0.1.0_amd64.deb) | [Debian package](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge_0.1.0_arm64.deb) |
| Fedora | [RPM package](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge-0.1.0-1.x86_64.rpm) | [RPM package](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge-0.1.0-1.aarch64.rpm) |
| Omarchy / Arch | [Arch package](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge-0.1.0-1-x86_64.pkg.tar.zst) | [Arch package](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge-0.1.0-1-aarch64.pkg.tar.zst) |
| Other glibc 2.35+ distributions | [Portable archive](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge-0.1.0-linux-amd64.tar.gz) | [Portable archive](https://github.com/gerdums/pomeforge/releases/download/v0.1.0/pomeforge-0.1.0-linux-arm64.tar.gz) |

Open a `.deb` or `.rpm` with your system package installer. If your desktop does not have one, use the corresponding command in Downloads:

```sh
# Ubuntu / Debian / Mint
sudo apt update && sudo apt install ./pomeforge_0.1.0_amd64.deb
# Fedora
sudo dnf install ./pomeforge-0.1.0-1.x86_64.rpm
# Omarchy / Arch
sudo pacman -U ./pomeforge-0.1.0-1-x86_64.pkg.tar.zst
```

Use the ARM64 filename instead when appropriate. For the portable archive, extract it and run `python3 install.py` from its folder; see the included README for system dependencies. Open **Pomeforge Setup** from your applications menu, or run `pomeforge quickstart`.

## Run Hello World on your iPhone or iPad

The setup wizard handles the Linux tools and creates a SwiftUI Hello World project. Its first run downloads about 1.1 GB of Swift tools; Apple's SDK download is separate. Follow its prompts to:

1. Download the Xcode 26 `.xip` from [Apple](https://developer.apple.com/download/all/?q=Xcode) in your Linux browser, then paste its path into the wizard. Pomeforge extracts the SDK on Linux. Xcode itself never runs.
2. Plug in your unlocked iPhone or iPad with a data cable and accept **Trust This Computer**.
3. Sign in to Apple interactively, then approve the development install. Enable **Settings > Privacy & Security > Developer Mode** when prompted and tap the installed app.

Apple sign-in, account eligibility, SDK terms, device trust, and Developer Mode require your participation. A free Apple account uses xtool's password/2FA path; the public API-key path requires paid Apple Developer Program membership. Do not share your Apple credentials with an AI assistant.

Pomeforge 0.1.0 is an early release. Linux compilation is verified; a complete physical iPhone/iPad run and App Store acceptance have not yet been verified. See [tested environments and limits](docs/verification.md) and [device troubleshooting](docs/devices.md).

## Give these instructions to your AI assistant

Copy this entire block into an AI assistant that can use a terminal on your Linux computer:

```text
Install Pomeforge 0.1.0 from https://github.com/gerdums/pomeforge and help me run its SwiftUI Hello World starter on my physical iPhone or iPad. Every build step must run on Linux.

Detect my distribution from /etc/os-release and CPU from uname -m. Read the repository README and release notes. Download the matching official v0.1.0 package and SHA256SUMS from that GitHub release, verify the package checksum, and install with my distro's package manager. Explain any administrator password prompt; let me enter it. Do not pipe downloaded scripts into a shell or install an unverified third-party mirror.

Run Pomeforge as my normal user, never with sudo. Run pomeforge version, pomeforge schema --json and pomeforge quickstart --help. Start the interactive quickstart in a terminal I control. Let me obtain Apple's Xcode XIP through Apple's authenticated download page and enter any Apple credentials directly into the tool. Never ask me to paste passwords, API keys, 2FA codes, private keys, pairing records or device identifiers into this chat or put them in commands, project files or logs.

Use Pomeforge's documented commands and JSON plans. Preserve my existing projects and toolchains. Help me respond to device trust and Developer Mode prompts. Stop for my decision if xtool asks to revoke a certificate. Do not publish an app, upload an IPA, submit to App Review or change unrelated Apple resources. Verify the app actually opens on my device before reporting that the device test succeeded. Report missing prerequisites honestly; do not substitute a Mac or a hosted Mac build.
```

## Keep building

Open **Pomeforge Workspace** for the graphical app. Edit the generated Swift files in your preferred editor. Agents can inspect `pomeforge schema --json`, diagnose with `pomeforge doctor --json`, and preview actions with `pomeforge plan` before execution.

[Setup](docs/setup.md) · [CLI](docs/cli.md) · [Graphical workspace](docs/app.md) · [Devices](docs/devices.md) · [App Store distribution](docs/release.md) · [Architecture](docs/architecture.md) · [Roadmap](docs/implementation-plan.md)

The initial project format is Swift Package Manager with SwiftUI. Arbitrary Xcode projects, Flutter, React Native, Interface Builder and iOS Simulator execution are not supported by this release.

## Develop Pomeforge

On Linux with Go 1.24 or later:

```sh
make check
./bin/pomeforge app --workspace "$HOME/Pomeforge" --listen 127.0.0.1:0 --open
```

## License

Pomeforge's original code and documentation are [MIT licensed](LICENSE). [Third-party components](THIRD_PARTY_NOTICES.md) retain their own licenses. Apple SDKs and account credentials are never included in the release.
