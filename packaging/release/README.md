# Linux release bundles

The native packages and portable bundle contain Pomeforge, its Linux AssetKit helper, unxip, license texts, and the exact corresponding unxip source. They do not include an Apple SDK or account credentials. Both Swift helpers include the Swift standard library; using the package does not require compiling source or running a container.

Use the package matching your architecture. The supported baseline is glibc 2.35 and the GCC 12 C++ runtime. The package manager installs system dependencies. Portable users must install equivalent dependencies themselves: Python 3, xdg-utils, xterm for the terminal setup menu, a Linux C compiler and headers, and the shared libraries used by Swift (libstdc++, libgcc, liblzma, zlib, libxml2.so.2, libcurl, libedit, SQLite, ncurses/tinfo, and libuuid). On Ubuntu/Debian, install `build-essential python3 xdg-utils xterm libstdc++6 liblzma5 zlib1g libxml2 libcurl4 libedit2 libsqlite3-0 libncurses6 libtinfo6 libuuid1 usbmuxd libimobiledevice-utils`. Arch needs `libxml2-legacy` for the required XML soname. A browser is needed for the graphical workspace. Older glibc and musl distributions cannot use these prebuilt Swift tools.

After extracting the portable archive, run `python3 install.py` as your normal user. It installs under the absolute `XDG_DATA_HOME` (or `~/.local/share`) and adds links in `~/.local/bin`. An optional `--prefix /absolute/user-owned/directory` selects the application directory. Existing unrelated commands or a different application bundle are not overwritten. Add `~/.local/bin` to `PATH` if your distribution has not already done so. Then run `pomeforge quickstart`, or open **Pomeforge Setup** from the desktop menu.

Quickstart asks before installing the pinned Swift 6.3.3 compiler. `pomeforge-runtime install` performs the same explicit download without an interactive prompt; use it only when you intend that action. It downloads approximately 1 GiB, verifies the release SHA-256, and needs at least 6 GiB free while staging the compiler. It installs only in private user-owned state, preserves upstream notices, and never invokes a package manager. `pomeforge-runtime status` checks the installed files and host requirements. Ordinary CLI or desktop startup does not download the compiler. The user supplies Xcode/SDK files separately through the documented SDK import flow.

Native packages install application files under `/opt/pomeforge`, command links in `/usr/bin`, and two menu entries. Package installation/removal has no network hooks and does not run the application as root. Package removal leaves user projects, SDKs, runtime downloads, and account state intact. To remove a portable application, remove only its selected application directory, its two `~/.local/bin` links, and `pomeforge.desktop`/`pomeforge-setup.desktop` from your XDG applications directory. User data remains separately under the XDG Pomeforge directories.

## Rebuilding the release

All compilation and packaging runs on Linux. Use the architecture-native pinned image `docker.io/library/swift:6.3.3-jammy@sha256:a05aa080e573b1f7e10bd630ce9e1d7645abb08a32d1def73cb611ac708d2a8a`, Go 1.24.13, and Python 3, Git, binutils, rpm, dpkg-dev, GNU tar, zstd, liblzma-dev, and zlib1g-dev. In a clean source checkout, run:

```sh
python3 packaging/release/build.py \
  --version 0.1.0 --arch arm64 --swift-root / --output-dir /tmp/pomeforge-release
```

Use `--arch amd64` on an amd64 Linux host. The builder verifies the source version, native architecture, Go and Swift versions, and the resolved AssetKit dependencies. It builds the CLI with CGO disabled and both Swift helpers with `--static-swift-stdlib`, strips native helpers, and checks their actual shared-library requirements. It emits `.deb`, `.rpm`, `.pkg.tar.zst`, and portable `.tar.gz` files, `SHA256SUMS-ARCH`, and `provenance-ARCH.json`. It refuses existing output filenames.

unxip is the unmodified upstream source at revision `6c3990517fcc4c1db6952fccf4c562fb14097601` (3.3.0). The corresponding source is retained in `share/sources/unxip-3.3.0.tar.gz`. To rebuild it on the same pinned Linux toolchain, extract that archive and run `swift build --configuration release --static-swift-stdlib`, with liblzma and zlib development headers installed. Its LGPLv3 and GPLv3 texts, plus the other embedded component notices, are in `share/docs/third-party`. The full Pomeforge source and build recipe are available at [gerdums/pomeforge](https://github.com/gerdums/pomeforge).

The release builder also downloads two checksum-pinned Ubuntu Jammy ncurses packages and bundles their narrow ncurses/tinfo libraries with the upstream copyright notice. On a fresh compiler install, verified copies go into Swift's existing private library search directory and are recorded in the runtime receipt. This supplies the narrow ABI on distributions such as Arch without changing system library links or Swift executables. Existing verified compiler installations are reused unchanged. The included debugger and standalone upstream tools are not separately validated by this compatibility check; the distro smoke test exercises SwiftPM's normal executable build and run.
