#!/usr/bin/env python3
"""Build native Linux release packages inside the pinned Swift Jammy image."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

UNXIP_REVISION = "6c3990517fcc4c1db6952fccf4c562fb14097601"
UNXIP_VERSION = "3.3.0"
SWIFT_VERSION = "6.3.3"
GO_VERSION = "go1.24.13"
SWIFT_IMAGE = "docker.io/library/swift:6.3.3-jammy@sha256:a05aa080e573b1f7e10bd630ce9e1d7645abb08a32d1def73cb611ac708d2a8a"


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def run(argv, cwd=None, env=None):
    print("+ " + " ".join(map(str, argv)), flush=True)
    result = subprocess.run(list(map(str, argv)), cwd=cwd, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if result.stdout:
        print(result.stdout, end="", flush=True)
    if result.returncode:
        raise RuntimeError("Command failed with exit " + str(result.returncode) + ": " + str(argv[0]))
    return result.stdout.strip()


def git(repo, *args):
    return run(["git", "-c", "safe.directory=" + str(repo), "-C", repo, *args])


def copy_file(source, destination, mode=0o644):
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)
    destination.chmod(mode)


def write_file(path, text, mode=0o644):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    path.chmod(mode)


def desktop_entries(root, prefix="/opt/pomeforge"):
    base = "[Desktop Entry]\nType=Application\nVersion=1.0\nIcon=pomeforge\nCategories=Development;IDE;\n"
    write_file(root / "usr/share/applications/pomeforge.desktop", base + "Name=Pomeforge Workspace\nComment=Build iPhone and iPad apps from Linux\nExec=" + prefix + "/libexec/pomeforge-desktop\nTerminal=false\nStartupNotify=true\n")
    write_file(root / "usr/share/applications/pomeforge-setup.desktop", base + "Name=Pomeforge Setup\nComment=Set up Linux tools and create HelloWorld\nExec=" + prefix + "/libexec/pomeforge-setup\nTerminal=true\nStartupNotify=false\n")


def bundle_manifest(bundle):
    files = {}
    for path in sorted(bundle.rglob("*")):
        relative = path.relative_to(bundle).as_posix()
        if path.is_symlink():
            files[relative] = {"symlink": os.readlink(path)}
        elif path.is_file():
            files[relative] = {"sha256": sha256(path), "mode": path.stat().st_mode & 0o777, "size": path.stat().st_size}
    write_file(bundle / "share/package-manifest.json", json.dumps(files, indent=2, sort_keys=True) + "\n")


def tar_gz(source, output, epoch):
    with open(output, "xb") as target, gzip.GzipFile(fileobj=target, mode="wb", filename="", mtime=epoch, compresslevel=9) as compressed:
        with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.PAX_FORMAT) as archive:
            for path in [source] + sorted(source.rglob("*")):
                info = archive.gettarinfo(str(path), arcname=str(path.relative_to(source.parent)))
                info.uid = info.gid = 0
                info.uname = info.gname = "root"
                info.mtime = epoch
                if info.isfile():
                    with open(path, "rb") as stream:
                        archive.addfile(info, stream)
                else:
                    archive.addfile(info)


def package_deb(root, output, version, arch):
    size = sum(p.stat().st_size for p in root.rglob("*") if p.is_file() and not p.is_symlink()) // 1024
    dependencies = "libc6 (>= 2.35), libstdc++6 (>= 12), libgcc-s1, liblzma5, zlib1g, libxml2, libcurl4, libedit2, libsqlite3-0, libncurses6, libtinfo6, libuuid1, python3, ca-certificates, xdg-utils, xterm, build-essential, usbmuxd, libimobiledevice-utils"
    control = "Package: pomeforge\nVersion: " + version + "\nArchitecture: " + arch + "\nMaintainer: Pomeforge contributors\nSection: devel\nPriority: optional\nHomepage: https://github.com/gerdums/pomeforge\nInstalled-Size: " + str(size) + "\nDepends: " + dependencies + "\nDescription: Linux workspace and CLI for iPhone and iPad development\n Includes native tools and guided, explicit first-run compiler setup.\n Apple SDKs and account credentials are supplied separately by the user.\n"
    write_file(root / "DEBIAN/control", control)
    run(["dpkg-deb", "--root-owner-group", "--build", root, output])
    shutil.rmtree(root / "DEBIAN")


def package_rpm(root, output, version, arch, temporary):
    rpm_arch = {"amd64": "x86_64", "arm64": "aarch64"}[arch]
    top = temporary / "rpm"
    for name in ("BUILD", "BUILDROOT", "RPMS", "SOURCES", "SPECS", "SRPMS"):
        (top / name).mkdir(parents=True)
    spec = top / "SPECS/pomeforge.spec"
    requires = "glibc >= 2.35, libstdc++ >= 12, libgcc, xz-libs, zlib, libxml2.so.2()(64bit), libcurl, libedit, sqlite-libs, ncurses-libs, libuuid, python3, ca-certificates, xdg-utils, xterm, gcc-c++, glibc-devel, binutils, usbmuxd, libimobiledevice-utils"
    # All content was built/stripped explicitly. Disable implicit rpmbuild
    # transformations so package bytes match the retained bundle manifest.
    write_file(spec, """%global __os_install_post %{nil}
%global _build_id_links none
%global debug_package %{nil}
Name: pomeforge
Version: VERSION
Release: 1
Summary: Linux workspace and CLI for iPhone and iPad development
License: MIT AND LGPL-3.0-or-later AND Apache-2.0
URL: https://github.com/gerdums/pomeforge
BuildArch: ARCH
AutoReqProv: no
Requires: REQUIRES

%description
Native Linux tools and guided first-run compiler setup. Apple SDKs and
account credentials are supplied separately by the user.

%install
mkdir -p "%{buildroot}"
cp -a "ROOT/." "%{buildroot}/"

%files
/opt/pomeforge
/usr/bin/pomeforge
/usr/bin/pomeforge-runtime
/usr/share/applications/pomeforge.desktop
/usr/share/applications/pomeforge-setup.desktop
/usr/share/icons/hicolor/scalable/apps/pomeforge.svg
""".replace("VERSION", version).replace("ARCH", rpm_arch).replace("REQUIRES", requires).replace("ROOT", str(root)))
    run(["rpmbuild", "-bb", "--define", "_topdir " + str(top), "--target", rpm_arch, spec])
    built = list((top / "RPMS").rglob("*.rpm"))
    if len(built) != 1:
        raise RuntimeError("Expected exactly one RPM package.")
    shutil.copyfile(built[0], output)


def package_arch(root, output, version, arch, epoch):
    arch_name = {"amd64": "x86_64", "arm64": "aarch64"}[arch]
    size = sum(p.stat().st_size for p in root.rglob("*") if p.is_file() and not p.is_symlink())
    dependencies = ["glibc>=2.35", "gcc-libs>=12", "xz", "zlib", "libxml2-legacy", "curl", "libedit", "sqlite", "ncurses", "util-linux-libs", "python", "ca-certificates", "xdg-utils", "xterm", "base-devel", "usbmuxd", "libimobiledevice"]
    info = "pkgname = pomeforge\npkgbase = pomeforge\npkgver = " + version + "-1\npkgdesc = Linux workspace and CLI for iPhone and iPad development\nurl = https://github.com/gerdums/pomeforge\nbuilddate = " + str(epoch) + "\npackager = Pomeforge contributors\nsize = " + str(size) + "\narch = " + arch_name + "\nlicense = MIT\nlicense = LGPL-3.0-or-later\nlicense = Apache-2.0\n" + "".join("depend = " + item + "\n" for item in dependencies)
    write_file(root / ".PKGINFO", info)
    run(["tar", "--sort=name", "--mtime=@" + str(epoch), "--owner=0", "--group=0", "--numeric-owner", "-I", "zstd -19 -T0", "-cf", output, "-C", root, ".PKGINFO", "opt", "usr"])
    (root / ".PKGINFO").unlink()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--arch", required=True, choices=["amd64", "arm64"])
    parser.add_argument("--swift-root", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()
    actual_arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or actual_arch != args.arch:
        raise RuntimeError("Release packages must be built natively on Linux for the selected architecture.")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", args.version):
        raise RuntimeError("Release version must have three numeric components.")
    source = Path(__file__).resolve().parents[2]
    source_version = re.search(r'\bVersion\s*=\s*"([^"]+)"', (source / "internal/pomeforge/types.go").read_text())
    if not source_version or source_version.group(1) != args.version:
        raise RuntimeError("Requested release version does not match the source Version constant.")
    revision = git(source, "rev-parse", "HEAD")
    if git(source, "status", "--porcelain"):
        raise RuntimeError("Commit all source changes before building release packages.")
    epoch = int(git(source, "show", "-s", "--format=%ct", "HEAD"))
    for name in ("go", "git", "strip", "ldd", "readelf", "dpkg-deb", "rpmbuild", "tar", "zstd"):
        if not shutil.which(name):
            raise RuntimeError("Missing release build dependency: " + name)
    environment = dict(os.environ, GOTOOLCHAIN="local", CGO_ENABLED="0", GOOS="linux", GOARCH=args.arch, SOURCE_DATE_EPOCH=str(epoch))
    go_version = run(["go", "version"], env=environment)
    if not re.search(r"\b" + re.escape(GO_VERSION) + r"\b", go_version):
        raise RuntimeError("Use the pinned " + GO_VERSION + " toolchain.")
    swift = args.swift_root.resolve() / "usr/bin/swift"
    swift_version = run([swift, "--version"])
    if "Swift version " + SWIFT_VERSION not in swift_version:
        raise RuntimeError("Use the pinned Swift " + SWIFT_VERSION + " Jammy toolchain.")
    args.output_dir.mkdir(parents=True, exist_ok=True)
    output_dir = args.output_dir.resolve()
    filenames = ["pomeforge_" + args.version + "_" + args.arch + ".deb", "pomeforge-" + args.version + "-1." + {"amd64": "x86_64", "arm64": "aarch64"}[args.arch] + ".rpm", "pomeforge-" + args.version + "-1-" + {"amd64": "x86_64", "arm64": "aarch64"}[args.arch] + ".pkg.tar.zst", "pomeforge-" + args.version + "-linux-" + args.arch + ".tar.gz"]
    for filename in filenames + ["SHA256SUMS-" + args.arch, "provenance-" + args.arch + ".json"]:
        if (output_dir / filename).exists():
            raise RuntimeError("Refusing to overwrite an existing release artifact: " + filename)
    with tempfile.TemporaryDirectory(prefix="pomeforge-release-") as temporary_name:
        temporary = Path(temporary_name)
        bundle = temporary / ("pomeforge-" + args.version + "-linux-" + args.arch)
        (bundle / "libexec").mkdir(parents=True)
        run(["go", "build", "-trimpath", "-buildvcs=false", "-o", bundle / "libexec/pomeforge", "./cmd/pomeforge"], cwd=source, env=environment)
        if run([bundle / "libexec/pomeforge", "version"]) != "Pomeforge " + args.version:
            raise RuntimeError("Built CLI reports the wrong version.")
        assets_source = temporary / "asset-compiler"
        shutil.copytree(source / "tools/asset-compiler", assets_source, ignore=shutil.ignore_patterns(".build", ".swiftpm"))
        resolved_hash = sha256(assets_source / "Package.resolved")
        run([swift, "build", "--package-path", assets_source, "--configuration", "release", "--static-swift-stdlib", "--disable-automatic-resolution"])
        if sha256(assets_source / "Package.resolved") != resolved_hash:
            raise RuntimeError("Asset compiler resolved dependencies changed during the build.")
        copy_file(assets_source / ".build/release/pomeforge-assets", bundle / "libexec/pomeforge-assets", 0o755)
        unxip_source = temporary / "unxip"
        run(["git", "init", "--quiet", unxip_source])
        git(unxip_source, "remote", "add", "origin", "https://github.com/saagarjha/unxip.git")
        git(unxip_source, "fetch", "--depth=1", "origin", UNXIP_REVISION)
        if git(unxip_source, "rev-parse", "FETCH_HEAD") != UNXIP_REVISION:
            raise RuntimeError("Unxip source revision mismatch.")
        git(unxip_source, "checkout", "--quiet", "--detach", "FETCH_HEAD")
        run([swift, "build", "--package-path", unxip_source, "--configuration", "release", "--static-swift-stdlib"])
        copy_file(unxip_source / ".build/release/unxip", bundle / "libexec/unxip", 0o755)
        source_tar = temporary / "unxip-source.tar"
        git(unxip_source, "archive", "--format=tar", "--prefix=unxip-" + UNXIP_VERSION + "/", "--output=" + str(source_tar), UNXIP_REVISION)
        archive_path = bundle / ("share/sources/unxip-" + UNXIP_VERSION + ".tar.gz")
        archive_path.parent.mkdir(parents=True)
        with open(source_tar, "rb") as stream, open(archive_path, "xb") as target, gzip.GzipFile(fileobj=target, mode="wb", filename="", mtime=epoch) as compressed:
            shutil.copyfileobj(stream, compressed)
        dependencies = {}
        helper_abi = {}
        for name in ("pomeforge-assets", "unxip"):
            binary = bundle / "libexec" / name
            run(["strip", "--strip-unneeded", binary])
            with open(binary, "rb") as stream:
                elf = stream.read(20)
            expected_machine = {"amd64": 62, "arm64": 183}[args.arch]
            if elf[:6] != b"\x7fELF\x02\x01" or int.from_bytes(elf[18:20], "little") != expected_machine:
                raise RuntimeError("Helper architecture does not match the native release target.")
            versions = run(["readelf", "--version-info", binary])
            glibc_versions = set(re.findall(r"\bGLIBC_([0-9]+(?:\.[0-9]+)+)", versions))
            if not glibc_versions or any(tuple(map(int, version.split("."))) > (2, 35) for version in glibc_versions):
                raise RuntimeError("Helper exceeds the supported glibc 2.35 baseline.")
            helper_abi[name] = {"machine": expected_machine, "maximumGlibc": max(glibc_versions, key=lambda value: tuple(map(int, value.split("."))))}
            dependencies[name] = run(["ldd", binary])
            if "not found" in dependencies[name] or re.search(r"lib(?:swift|Foundation|dispatch)", dependencies[name]):
                raise RuntimeError("Static Swift helper has unresolved or shared Swift runtime dependencies.")
            if name == "unxip":
                run([binary, "--version"])
            run([binary, "--help"])
        for name in ("pomeforge-runtime", "pomeforge-desktop", "pomeforge-setup"):
            copy_file(source / "packaging/release" / name, bundle / "libexec" / name, 0o755)
        copy_file(source / "packaging/release/runtime-lock.json", bundle / "libexec/runtime-lock.json")
        copy_file(source / "packaging/release/pomeforge", bundle / "bin/pomeforge", 0o755)
        (bundle / "bin/pomeforge-runtime").symlink_to("../libexec/pomeforge-runtime")
        copy_file(source / "packaging/release/install.py", bundle / "install.py", 0o755)
        copy_file(source / "packaging/release/README.md", bundle / "README.md")
        for name in ("LICENSE", "THIRD_PARTY_NOTICES.md", "toolchains.lock.json"):
            copy_file(source / name, bundle / "share" / name)
        shutil.copytree(source / "docs/third-party", bundle / "share/docs/third-party")
        copy_file(source / "packaging/pomeforge.svg", bundle / "share/pomeforge.svg")
        copy_file(source / "packaging/release/README.md", bundle / "share/sources/BUILD.md")
        bundle_manifest(bundle)
        native = temporary / "native"
        shutil.copytree(bundle, native / "opt/pomeforge", symlinks=True)
        (native / "usr/bin").mkdir(parents=True)
        (native / "usr/bin/pomeforge").symlink_to("/opt/pomeforge/bin/pomeforge")
        (native / "usr/bin/pomeforge-runtime").symlink_to("/opt/pomeforge/libexec/pomeforge-runtime")
        desktop_entries(native)
        copy_file(source / "packaging/pomeforge.svg", native / "usr/share/icons/hicolor/scalable/apps/pomeforge.svg")
        package_deb(native, output_dir / filenames[0], args.version, args.arch)
        package_rpm(native, output_dir / filenames[1], args.version, args.arch, temporary)
        package_arch(native, output_dir / filenames[2], args.version, args.arch, epoch)
        tar_gz(bundle, output_dir / filenames[3], epoch)
        hashes = {name: sha256(output_dir / name) for name in filenames}
        write_file(output_dir / ("SHA256SUMS-" + args.arch), "".join(value + "  " + name + "\n" for name, value in hashes.items()))
        provenance = {"sourceRevision": revision, "version": args.version, "architecture": args.arch, "go": go_version, "swift": swift_version, "swiftImage": SWIFT_IMAGE, "unxipRevision": UNXIP_REVISION, "assetCompilerResolvedSHA256": resolved_hash, "staticSwiftStdlib": True, "helperDependencies": dependencies, "helperABI": helper_abi, "artifacts": hashes, "buildOS": "Linux"}
        write_file(output_dir / ("provenance-" + args.arch + ".json"), json.dumps(provenance, indent=2) + "\n")
    print("Built " + ", ".join(filenames))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print("Pomeforge release build: " + str(error), file=sys.stderr)
        sys.exit(1)
