#!/usr/bin/env python3
"""Install an extracted portable bundle for the current Linux user."""
import argparse
import ctypes
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import stat
import sys
import tempfile


def sha256(path):
    result = hashlib.sha256()
    with open(path, "rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def verify(bundle):
    manifest = json.loads((bundle / "share/package-manifest.json").read_text())
    for relative, expected in manifest.items():
        if relative.startswith("/") or any(part in ("", ".", "..") for part in relative.split("/")):
            raise RuntimeError("Invalid portable bundle manifest path.")
        path = bundle / relative
        if "symlink" in expected:
            if not path.is_symlink() or os.readlink(path) != expected["symlink"] or not path.resolve().is_relative_to(bundle.resolve()):
                raise RuntimeError("Portable bundle link changed: " + relative)
        elif not path.is_file() or path.is_symlink() or sha256(path) != expected["sha256"] or stat.S_IMODE(path.stat().st_mode) != expected["mode"]:
            raise RuntimeError("Portable bundle file changed: " + relative)
    expected_files = set(manifest) | {"share/package-manifest.json"}
    actual = {p.relative_to(bundle).as_posix() for p in bundle.rglob("*") if p.is_file() or p.is_symlink()}
    if actual != expected_files:
        raise RuntimeError("Portable bundle has unexpected or missing files.")


def desktop_exec(path):
    value = str(path)
    if any(ord(c) < 32 or ord(c) > 126 for c in value) or "%" in value or "=" in value:
        raise RuntimeError("Install location cannot be represented safely in a desktop entry.")
    # General desktop string escaping is processed before Exec quoting.
    return '"' + "".join("\\\\\\\\" if c == "\\" else "\\\\" + c if c in ('"', "$", "`") else c for c in value) + '"'


def integration_matches(path, contents):
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        if path.is_symlink():
            raise RuntimeError("Refusing to replace an existing integration file: " + str(path))
        return False
    except OSError as error:
        raise RuntimeError("Cannot safely read integration destination: " + str(path)) from error
    with os.fdopen(descriptor, "rb") as stream:
        metadata = os.fstat(stream.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_size != len(contents) or stream.read(len(contents) + 1) != contents:
            raise RuntimeError("Refusing to replace an existing integration file: " + str(path))
    return True


def install_integration_file(path, contents):
    if integration_matches(path, contents):
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    staged = None
    try:
        with tempfile.NamedTemporaryFile(mode="wb", dir=path.parent, prefix=".pomeforge-", delete=False) as stream:
            staged = Path(stream.name)
            stream.write(contents)
        staged.chmod(0o644)
        try:
            os.link(staged, path)
        except FileExistsError:
            # A concurrent installation may have published identical bytes.
            # An unrelated file or symlink must never be replaced.
            if not integration_matches(path, contents):
                raise RuntimeError("Integration destination changed during installation: " + str(path))
    finally:
        if staged is not None:
            staged.unlink()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prefix", type=Path, help="User-owned application directory (must not contain unrelated files)")
    args = parser.parse_args()
    if platform.system() != "Linux" or os.geteuid() == 0:
        raise RuntimeError("Run this installer as your normal Linux user, without sudo.")
    bundle = Path(__file__).resolve().parent
    verify(bundle)
    machine = {"x86_64": 62, "aarch64": 183, "arm64": 183}.get(platform.machine())
    with open(bundle / "libexec/pomeforge", "rb") as source:
        header = source.read(20)
    if not machine or header[:6] != b"\x7fELF\x02\x01" or int.from_bytes(header[18:20], "little") != machine:
        raise RuntimeError("This portable bundle does not match your Linux architecture.")
    home = Path(os.environ.get("HOME", ""))
    if not home.is_absolute():
        raise RuntimeError("An absolute HOME is required.")
    xdg = os.environ.get("XDG_DATA_HOME", "")
    data = Path(xdg) if xdg.startswith("/") else home / ".local/share"
    prefix = (args.prefix if args.prefix is not None else data / "pomeforge/application").absolute()
    if prefix.is_symlink():
        raise RuntimeError("The install destination cannot be a symlink.")
    if prefix.resolve() != bundle and prefix.resolve().is_relative_to(bundle):
        raise RuntimeError("The install destination cannot be inside the source bundle.")
    exec_gui = desktop_exec(prefix / "libexec/pomeforge-desktop")
    exec_setup = desktop_exec(prefix / "libexec/pomeforge-setup")
    user_bin = home / ".local/bin"
    entries = {"pomeforge": prefix / "bin/pomeforge", "pomeforge-runtime": prefix / "libexec/pomeforge-runtime"}
    for name, target in entries.items():
        link = user_bin / name
        if (link.exists() or link.is_symlink()) and (not link.is_symlink() or os.readlink(link) != str(target)):
            raise RuntimeError("Refusing to replace an existing command: " + str(link))
    applications = data / "applications"
    base = "[Desktop Entry]\nType=Application\nVersion=1.0\nIcon=pomeforge\nCategories=Development;IDE;\n"
    integration = {
        data / "icons/hicolor/scalable/apps/pomeforge.svg": (bundle / "share/pomeforge.svg").read_bytes(),
        applications / "pomeforge.desktop": (base + "Name=Pomeforge Workspace\nExec=" + exec_gui + "\nTerminal=false\nStartupNotify=true\n").encode(),
        applications / "pomeforge-setup.desktop": (base + "Name=Pomeforge Setup\nExec=" + exec_setup + "\nTerminal=true\nStartupNotify=false\n").encode(),
    }
    for destination, contents in integration.items():
        integration_matches(destination, contents)
    if prefix.exists():
        verify(prefix)
        if sha256(prefix / "share/package-manifest.json") != sha256(bundle / "share/package-manifest.json"):
            raise RuntimeError("A different bundle already occupies this destination; select a new --prefix.")
    else:
        prefix.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix=".pomeforge-install-", dir=prefix.parent) as temporary:
            stage = Path(temporary) / "application"
            shutil.copytree(bundle, stage, symlinks=True)
            verify(stage)
            libc = ctypes.CDLL(None, use_errno=True)
            rename = libc.renameat2
            rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
            if rename(-100, os.fsencode(stage), -100, os.fsencode(prefix), 1):
                code = ctypes.get_errno()
                raise OSError(code, os.strerror(code), str(prefix))
    user_bin.mkdir(parents=True, exist_ok=True)
    for name, target in entries.items():
        link = user_bin / name
        if not link.is_symlink():
            link.symlink_to(target)
    for destination, contents in integration.items():
        install_integration_file(destination, contents)
    print("Pomeforge is installed at " + str(prefix))
    print("If needed, add commands to this terminal: export PATH=" + shlex.quote(str(user_bin)) + ':"$PATH"')
    print("Start setup: " + shlex.quote(str(user_bin / "pomeforge")) + " quickstart")
    print("No compiler download, Apple SDK import, or account operation was performed.")
    print("Removing the application directory and these two command links leaves your projects, SDKs, and runtime state intact.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError) as error:
        print("Pomeforge portable install: " + str(error), file=sys.stderr)
        sys.exit(1)
