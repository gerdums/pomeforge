#!/usr/bin/env python3
"""Focused Linux checks for runtime archives and release launchers."""
import hashlib
import importlib.machinery
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
RELEASE = Path(__file__).resolve().parents[1] / "release"
runtime = importlib.machinery.SourceFileLoader("pomeforge_runtime_test", str(RELEASE / "pomeforge-runtime")).load_module()
installer = importlib.machinery.SourceFileLoader("pomeforge_installer_test", str(RELEASE / "install.py")).load_module()


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pomeforge-release-test-")
        self.root = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def archive(self, entries):
        path = self.root / "fixture.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            for name, kind, content in entries:
                member = tarfile.TarInfo(name)
                member.mode = 0o755
                if kind == "directory":
                    member.type = tarfile.DIRTYPE
                    archive.addfile(member)
                elif kind == "symlink":
                    member.type = tarfile.SYMTYPE
                    member.linkname = content
                    archive.addfile(member)
                elif kind == "hardlink":
                    member.type = tarfile.LNKTYPE
                    member.linkname = content
                    archive.addfile(member)
                elif kind == "fifo":
                    member.type = tarfile.FIFOTYPE
                    archive.addfile(member)
                else:
                    data = content.encode()
                    member.size = len(data)
                    archive.addfile(member, io.BytesIO(data))
        return path

    def test_preserves_real_swift_multicall_alias_shape(self):
        archive = self.archive([
            ("swift", "directory", ""), ("swift/usr", "directory", ""),
            ("swift/usr/bin", "directory", ""),
            ("swift/usr/bin/swift", "symlink", "swift-driver"),
            ("swift/usr/bin/swift-driver", "file", "native-fixture"),
            ("swift/usr/bin/swiftc", "symlink", "swift"),
        ])
        stage = self.root / "stage"
        stage.mkdir(mode=0o700)
        records = runtime.extract_verified(archive, stage, "swift")
        self.assertEqual((stage / "usr/bin/swiftc").read_text(), "native-fixture")
        self.assertEqual(records["usr/bin/swift"]["target"], "swift-driver")

    def test_rejects_archive_escapes_special_files_and_collisions(self):
        attacks = [
            [("swift/../../outside", "file", "bad")],
            [("/swift/file", "file", "bad")],
            [("swift/usr\\outside", "file", "bad")],
            [("swift/link", "symlink", "../../outside")],
            [("swift/link", "symlink", "/tmp/outside")],
            [("swift/pipe", "fifo", "")],
            [("swift/hard", "hardlink", "swift/file")],
            [("swift/file", "file", "x"), ("swift/file/", "directory", "")],
            [("swift/a", "symlink", "b"), ("swift/b", "symlink", "a")],
            [("swift/link", "symlink", "missing")],
            [("swift/d", "directory", ""), ("swift/link", "symlink", "d"), ("swift/link/file", "file", "x")],
        ]
        for entries in attacks:
            with self.subTest(entries=entries):
                archive = self.archive(entries)
                with tarfile.open(archive, "r:gz") as opened:
                    with self.assertRaises(RuntimeError):
                        runtime.inspect_members(opened, "swift")
        self.assertFalse((self.root / "outside").exists())

    def test_rejects_dotdot_after_intermediate_directory_symlink(self):
        outside = self.root / "escape"
        outside.write_text("outside sentinel")
        archive = self.archive([
            ("swift", "directory", ""),
            ("swift/deep", "directory", ""),
            ("swift/deep/dir", "directory", ""),
            ("swift/inside", "directory", ""),
            ("swift/deep/escape", "file", "lexical target"),
            ("swift/deep/dir/a", "symlink", "../../inside"),
            ("swift/deep/dir/b", "symlink", "a/../../escape"),
        ])
        stage = self.root / "stage"
        stage.mkdir(mode=0o700)
        with self.assertRaisesRegex(RuntimeError, "outside its root"):
            runtime.extract_verified(archive, stage, "swift")
        self.assertEqual(outside.read_text(), "outside sentinel")

    def test_receipt_rechecks_actual_symlink_resolution(self):
        stage, lock = self.installed_fixture()
        (self.root / "escape").write_text("outside sentinel")
        for relative in ("deep", "deep/dir", "inside"):
            (stage / relative).mkdir()
        (stage / "deep/escape").write_text("lexical target")
        (stage / "deep/dir/a").symlink_to("../../inside")
        (stage / "deep/dir/b").symlink_to("a/../../escape")
        receipt = json.loads((stage / runtime.RECEIPT).read_text())
        for relative in ("deep", "deep/dir", "inside"):
            receipt["files"][relative] = {"type": "directory"}
        path = stage / "deep/escape"
        receipt["files"]["deep/escape"] = {"type": "file", "size": path.stat().st_size, "mode": stat.S_IMODE(path.stat().st_mode), "sha256": runtime.digest(path)}
        receipt["files"]["deep/dir/a"] = {"type": "symlink", "target": "../../inside"}
        receipt["files"]["deep/dir/b"] = {"type": "symlink", "target": "a/../../escape"}
        (stage / runtime.RECEIPT).write_text(json.dumps(receipt))
        with self.assertRaisesRegex(RuntimeError, "runtime changed"):
            runtime.verify_tree(stage, "arm64", lock)
        self.assertEqual((self.root / "escape").read_text(), "outside sentinel")

    def installed_fixture(self):
        entries = [("swift", "directory", ""), ("swift/usr", "directory", ""), ("swift/usr/bin", "directory", "")]
        entries += [("swift/usr/bin/" + name, "file", "fixture") for name in ("swift", "swift-package", "swift-frontend", "clang")]
        archive = self.archive(entries)
        stage = self.root / "installed"
        stage.mkdir(mode=0o700)
        records = runtime.extract_verified(archive, stage, "swift")
        lock = {"version": "test", "assets": {"arm64": {"sha256": "fixture"}}}
        (stage / runtime.RECEIPT).write_text(json.dumps({"version": "test", "architecture": "arm64", "archiveSHA256": "fixture", "files": records}))
        return stage, lock

    def test_receipt_detects_changed_missing_extra_and_symlink_files(self):
        for mutation in ("changed", "missing", "extra", "symlink"):
            with self.subTest(mutation=mutation):
                stage, lock = self.installed_fixture()
                runtime.verify_tree(stage, "arm64", lock)
                swift = stage / "usr/bin/swift"
                if mutation == "changed":
                    swift.write_text("changed")
                elif mutation == "missing":
                    swift.unlink()
                elif mutation == "extra":
                    (stage / "unexpected").write_text("unexpected")
                else:
                    swift.unlink()
                    swift.symlink_to("clang")
                with self.assertRaises(RuntimeError):
                    runtime.verify_tree(stage, "arm64", lock)
                shutil.rmtree(stage)

    def test_activation_never_replaces_existing_directory(self):
        stage = self.root / "stage"
        destination = self.root / "occupied"
        stage.mkdir()
        destination.mkdir()
        with self.assertRaises(OSError):
            runtime.activate(stage, destination)
        self.assertTrue(stage.is_dir())
        self.assertTrue(destination.is_dir())

    def test_runtime_status_creates_no_state(self):
        home = self.root / "home"
        home.mkdir()
        environment = dict(os.environ, HOME=str(home), XDG_DATA_HOME="relative-ignored", PYTHONDONTWRITEBYTECODE="1")
        result = subprocess.run([sys.executable, str(RELEASE / "pomeforge-runtime"), "status"], env=environment, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(home.iterdir()), [])

    def test_managed_state_rejects_symlink_directory(self):
        real = self.root / "real"
        real.mkdir(mode=0o700)
        link = self.root / "link"
        link.symlink_to(real)
        with self.assertRaises(RuntimeError):
            runtime.private_directory(link)

    def test_download_hash_mismatch_fails_before_extraction(self):
        class Response(io.BytesIO):
            headers = {"Content-Length": "4"}
            def geturl(self):
                return "https://example.invalid/archive"
        with mock.patch.object(runtime.urllib.request, "urlopen", return_value=Response(b"data")):
            with self.assertRaisesRegex(RuntimeError, "integrity"):
                runtime.download({"url": "https://example.invalid/archive", "size": 4, "sha256": "0" * 64}, self.root / "download")

    def test_desktop_exec_applies_two_escape_layers(self):
        value = '/tmp/a "quote" \\ slash $dollar `tick`/command'
        escaped = installer.desktop_exec(value)
        # GKeyFile string unescaping precedes shell-like Exec argument parsing.
        decoded = escaped.replace("\\\\", "\\")
        self.assertIn('\\"quote\\"', decoded)
        self.assertIn("\\\\ slash", decoded)
        self.assertIn("\\$dollar", decoded)
        for bad in ("/tmp/percent%/app", "/tmp/equals=/app", "/tmp/new\nline/app"):
            with self.assertRaises(RuntimeError):
                installer.desktop_exec(bad)

    def launcher_fixture(self):
        bundle = self.root / 'bundle with "quotes" $and `ticks`'
        (bundle / "bin").mkdir(parents=True)
        (bundle / "libexec").mkdir()
        for name, destination in (("pomeforge", "bin/pomeforge"), ("pomeforge-desktop", "libexec/pomeforge-desktop"), ("pomeforge-setup", "libexec/pomeforge-setup")):
            shutil.copyfile(RELEASE / name, bundle / destination)
            (bundle / destination).chmod(0o755)
        child = bundle / "libexec/pomeforge"
        child.write_text('#!/bin/sh\nprintf "%s\\n" "$PATH" "$@"\nexit "${TEST_CHILD_EXIT:-0}"\n')
        child.chmod(0o755)
        home = self.root / "fresh-home"
        home.mkdir()
        return bundle, dict(os.environ, HOME=str(home), XDG_DATA_HOME="relative-ignored")

    def test_launcher_prefix_and_arguments_survive_relocation(self):
        bundle, env = self.launcher_fixture()
        result = subprocess.run([str(bundle / "bin/pomeforge"), "version", "argument with spaces"], env=env, capture_output=True, text=True, check=True)
        lines = result.stdout.splitlines()
        self.assertTrue(lines[0].startswith(env["HOME"] + "/.local/share/pomeforge/runtime/swift-6.3.3-ubuntu22.04-"))
        self.assertIn(str(bundle / "libexec"), lines[0])
        self.assertEqual(lines[1:], ["version", "argument with spaces"])
        self.assertFalse((Path(env["HOME"]) / ".local").exists())

    def test_workspace_menu_creates_fresh_default_directory(self):
        bundle, env = self.launcher_fixture()
        result = subprocess.run([str(bundle / "libexec/pomeforge-desktop")], env=env, capture_output=True, text=True, check=True)
        self.assertTrue((Path(env["HOME"]) / "PomeforgeProjects").is_dir())
        self.assertIn("--workspace\n" + env["HOME"] + "/PomeforgeProjects\n", result.stdout)

    def test_setup_menu_preserves_failed_child_status(self):
        bundle, env = self.launcher_fixture()
        env["TEST_CHILD_EXIT"] = "7"
        result = subprocess.run([str(bundle / "libexec/pomeforge-setup")], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 7)
        self.assertIn("quickstart", result.stdout)
        self.assertIn("status 7", result.stderr)

    def portable_fixture(self):
        bundle = self.root / "portable-source"
        (bundle / "bin").mkdir(parents=True)
        (bundle / "libexec").mkdir()
        (bundle / "share").mkdir()
        for name, destination in (("install.py", "install.py"), ("pomeforge", "bin/pomeforge"), ("pomeforge-runtime", "libexec/pomeforge-runtime"), ("pomeforge-desktop", "libexec/pomeforge-desktop"), ("pomeforge-setup", "libexec/pomeforge-setup")):
            shutil.copyfile(RELEASE / name, bundle / destination)
            (bundle / destination).chmod(0o755)
        # A native ELF fixture proves architecture validation and file-copy
        # behavior; no assertion here claims to execute the Pomeforge app.
        shutil.copyfile("/bin/true", bundle / "libexec/pomeforge")
        (bundle / "libexec/pomeforge").chmod(0o755)
        (bundle / "share/pomeforge.svg").write_text("<svg/>")
        files = {}
        for path in bundle.rglob("*"):
            if path.is_file():
                files[path.relative_to(bundle).as_posix()] = {"sha256": runtime.digest(path), "size": path.stat().st_size, "mode": stat.S_IMODE(path.stat().st_mode)}
        (bundle / "share/package-manifest.json").write_text(json.dumps(files))
        home = self.root / "portable-home"
        home.mkdir()
        return bundle, dict(os.environ, HOME=str(home), XDG_DATA_HOME="relative-ignored", PYTHONDONTWRITEBYTECODE="1")

    def test_portable_install_and_repeat_preserve_bundle(self):
        bundle, env = self.portable_fixture()
        prefix = self.root / 'installed with "quote" \\ $dollar `tick`'
        command = [sys.executable, str(bundle / "install.py"), "--prefix", str(prefix)]
        for _ in range(2):
            result = subprocess.run(command, env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
        installer.verify(bundle)
        installer.verify(prefix)
        home = Path(env["HOME"])
        self.assertEqual(os.readlink(home / ".local/bin/pomeforge"), str(prefix / "bin/pomeforge"))
        desktop = (home / ".local/share/applications/pomeforge-setup.desktop").read_text()
        self.assertIn("Terminal=true", desktop)
        self.assertIn(installer.desktop_exec(prefix / "libexec/pomeforge-setup"), desktop)

    def test_portable_refuses_recursive_destination_before_writes(self):
        bundle, env = self.portable_fixture()
        prefix = bundle / "nested-destination"
        result = subprocess.run([sys.executable, str(bundle / "install.py"), "--prefix", str(prefix)], env=env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("inside the source bundle", result.stderr)
        self.assertFalse(prefix.exists())
        installer.verify(bundle)

    def test_portable_preserves_existing_integration_files_before_writes(self):
        bundle, env = self.portable_fixture()
        prefix = self.root / "new-install"
        data = Path(env["HOME"]) / ".local/share"
        for relative in ("applications/pomeforge.desktop", "applications/pomeforge-setup.desktop", "icons/hicolor/scalable/apps/pomeforge.svg"):
            with self.subTest(relative=relative):
                destination = data / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(b"unrelated existing file")
                result = subprocess.run([sys.executable, str(bundle / "install.py"), "--prefix", str(prefix)], env=env, text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Refusing to replace", result.stderr)
                self.assertEqual(destination.read_bytes(), b"unrelated existing file")
                self.assertFalse(prefix.exists())
                self.assertFalse((Path(env["HOME"]) / ".local/bin").exists())
                destination.unlink()
        installer.verify(bundle)

    def test_integration_publication_does_not_replace_raced_file(self):
        destination = self.root / "pomeforge.desktop"
        original_link = os.link

        def publish_competing_file(source, target):
            destination.write_bytes(b"concurrent unrelated file")
            return original_link(source, target)

        with mock.patch.object(installer.os, "link", side_effect=publish_competing_file):
            with self.assertRaisesRegex(RuntimeError, "Refusing to replace"):
                installer.install_integration_file(destination, b"new desktop entry")
        self.assertEqual(destination.read_bytes(), b"concurrent unrelated file")
        self.assertEqual(list(self.root.iterdir()), [destination])


if __name__ == "__main__":
    if sys.platform != "linux":
        raise SystemExit("Run release checks on Linux.")
    unittest.main(verbosity=2)
