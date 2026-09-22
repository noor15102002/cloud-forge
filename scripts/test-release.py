#!/usr/bin/env python3
"""Release installation regressions; no runtime tools or application execution."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import subprocess

SPEC = importlib.util.spec_from_file_location("installer", Path(__file__).with_name("install-candidate.py"))
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)
VERSION, COMMIT, DATE = "v0.1.0-alpha.1", "a" * 40, "2026-09-22T00:00:00Z"


class CandidateInstallTests(unittest.TestCase):
    def candidate(self, root, *, identity=None, member="cloudforge", symbolic=False):
        source = root / "release"
        source.mkdir()
        identity = identity or {"schema_version": "v1alpha1", "version": VERSION, "commit": COMMIT, "date": DATE}
        payload = ("#!/bin/sh\nprintf '%s\\n' '" + json.dumps(identity) + "'\n").encode()
        archive = source / f"cloudforge_{VERSION}_linux_amd64.tar.gz"
        with tarfile.open(archive, mode="w:gz") as package:
            for name, data in ((member, payload), ("LICENSE", b"test license\n")):
                info = tarfile.TarInfo(name)
                info.size = len(data)
                if symbolic and name == member:
                    info.type, info.linkname = tarfile.SYMTYPE, "/tmp/unrelated"
                package.addfile(info, io.BytesIO(data))
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        manifest = {"version": VERSION, "commit": COMMIT, "date": DATE, "source_date_epoch": 1790035200,
                    "go_version": "go1.27.1", "os": "linux", "arch": "amd64", "archive": archive.name,
                    "archive_sha256": digest, "binary_sha256": hashlib.sha256(payload).hexdigest()}
        (source / "release.json").write_text(json.dumps(manifest))
        (source / "checksums.txt").write_text(f"{digest}  {archive.name}\n")
        return source, digest

    def attempt(self, root, source, digest):
        return installer.install(source, root / "installed", VERSION, COMMIT, DATE, digest)

    def test_clean_install_preserves_exact_binary_and_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            binary = self.attempt(root, source, digest)
            manifest = json.loads((source / "release.json").read_text())
            self.assertEqual(hashlib.sha256(binary.read_bytes()).hexdigest(), manifest["binary_sha256"])
            self.assertEqual(json.loads((binary.parent / "installation.json").read_text()), manifest)

    def test_archive_checksum_mismatch_rejected_before_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, _ = self.candidate(root)
            manifest = json.loads((source / "release.json").read_text())
            archive = source / manifest["archive"]
            archive.write_bytes(archive.read_bytes() + b"corruption")
            with self.assertRaisesRegex(ValueError, "archive checksum"):
                self.attempt(root, source, manifest["archive_sha256"])
            self.assertFalse((root / "installed/cloudforge").exists())

    def test_identity_mismatch_rejected(self):
        for key, value in (("version", "dev"), ("commit", "b" * 40), ("date", "unknown")):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                identity = {"schema_version": "v1alpha1", "version": VERSION, "commit": COMMIT, "date": DATE, key: value}
                source, digest = self.candidate(root, identity=identity)
                with self.assertRaisesRegex(ValueError, "binary identity mismatch"):
                    self.attempt(root, source, digest)
                self.assertFalse((root / "installed/cloudforge").exists())

    def test_duplicate_manifest_keys_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            path = source / "release.json"
            path.write_text(path.read_text().replace("{", '{"version":"dev",', 1))
            with self.assertRaisesRegex(ValueError, "duplicate release"):
                self.attempt(root, source, digest)

    def test_unsafe_archive_members_rejected(self):
        for member, symbolic in (("../cloudforge", False), ("cloudforge", True)):
            with self.subTest(member=member, symbolic=symbolic), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                source, digest = self.candidate(root, member=member, symbolic=symbolic)
                with self.assertRaisesRegex(ValueError, "archive member"):
                    self.attempt(root, source, digest)

    def test_full_expected_commit_required(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            with self.assertRaisesRegex(ValueError, "full source commit"):
                installer.install(source, root / "installed", VERSION, "a24b4e0", DATE, digest)

    def test_release_builder_refuses_uncommitted_source(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts"
            scripts.mkdir()
            builder = scripts / "build-release.sh"
            builder.write_bytes(Path(__file__).with_name("build-release.sh").read_bytes())
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            # Even an untracked source file must prevent a releasable build.
            result = subprocess.run(["bash", str(builder), VERSION, str(root / "dist")], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertIn("clean committed checkout", result.stderr)
            self.assertFalse((root / "dist").exists())


if __name__ == "__main__":
    unittest.main()
