#!/usr/bin/env python3
"""Release installation regressions; no runtime tools or application execution."""
import hashlib
import importlib.util
import io
import json
from datetime import datetime, timezone
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock
import subprocess
import copy
import shutil

from release_metadata import enrich_manifest, validate_manifest_shape

SPEC = importlib.util.spec_from_file_location("installer", Path(__file__).with_name("install-candidate.py"))
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)
COMPARE_SPEC = importlib.util.spec_from_file_location("compare_releases", Path(__file__).with_name("compare-releases.py"))
comparison = importlib.util.module_from_spec(COMPARE_SPEC)
COMPARE_SPEC.loader.exec_module(comparison)
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

    def assert_receipt(self, binary, manifest):
        receipt = json.loads((binary.parent / "installation.json").read_text())
        installed_at = receipt.pop("installed_at")
        self.assertRegex(installed_at, r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$")
        datetime.fromisoformat(installed_at.replace("Z", "+00:00"))
        self.assertEqual(receipt, manifest)
        return installed_at

    def test_clean_install_preserves_exact_binary_and_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            binary = self.attempt(root, source, digest)
            manifest = json.loads((source / "release.json").read_text())
            self.assertEqual(hashlib.sha256(binary.read_bytes()).hexdigest(), manifest["binary_sha256"])
            self.assert_receipt(binary, manifest)

    def test_installation_time_is_actual_receipt_metadata_without_changing_release(self):
        class InstallationClock(datetime):
            @classmethod
            def now(cls, tz=None):
                return cls(2026, 9, 23, 1, 2, 3, 456000, tzinfo=timezone.utc)

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            original_manifest = (source / "release.json").read_bytes()
            manifest = json.loads(original_manifest)
            with mock.patch.object(installer, "datetime", InstallationClock):
                binary = self.attempt(root, source, digest)
            self.assertEqual(self.assert_receipt(binary, manifest), "2026-09-23T01:02:03.456Z")
            self.assertEqual((source / "release.json").read_bytes(), original_manifest)
            self.assertEqual(hashlib.sha256((source / manifest["archive"]).read_bytes()).hexdigest(), digest)
            self.assertEqual(hashlib.sha256(binary.read_bytes()).hexdigest(), manifest["binary_sha256"])
            self.assertEqual(json.loads(subprocess.check_output([str(binary), "version", "--format", "json"]))["date"], DATE)

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

    def enriched(self, source):
        path = source / "release.json"
        manifest = enrich_manifest(json.loads(path.read_text()), "2026-09-22T01:00:00Z", "2026-09-22T01:00:05Z")
        path.write_text(json.dumps(manifest))
        return manifest

    def test_enriched_manifest_installs_and_legacy_remains_supported(self):
        for enriched in (False, True):
            with self.subTest(enriched=enriched), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                source, digest = self.candidate(root)
                if enriched:
                    self.enriched(source)
                binary = installer.install(source, root / "installed", VERSION, COMMIT, DATE, digest, require_enriched=enriched)
                self.assertTrue(binary.is_file())
                self.assert_receipt(binary, json.loads((source / "release.json").read_text()))

    def test_current_qualification_cannot_silently_use_legacy_manifest(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            with self.assertRaisesRegex(ValueError, "unsupported release manifest"):
                installer.install(source, root / "installed", VERSION, COMMIT, DATE, digest, require_enriched=True)

    def test_enriched_manifest_rejects_unknown_missing_or_misleading_metadata(self):
        with tempfile.TemporaryDirectory() as temporary:
            source, _ = self.candidate(Path(temporary))
            original = self.enriched(source)
            mutations = [
                lambda m: m.update(manifest_schema_version=3),
                lambda m: m.update(manifest_schema_version=True),
                lambda m: m.update(unrecognized=True),
                lambda m: m.pop("config_schemas"),
                lambda m: m["config_schemas"].update(current="v1alpha8"),
                lambda m: m["report_schemas"]["supported"].pop(),
                lambda m: m["expected_runtime_tools"]["pinned"].update(k3d="latest"),
                lambda m: m.update(qualification_build_started_at="2026-09-22T02:00:00Z"),
                lambda m: m.update(qualification_build_finished_at="2026-09-22T01:00:05+00:00"),
                lambda m: m.update(qualification_build_finished_at=None),
            ]
            for index, mutate in enumerate(mutations):
                with self.subTest(mutation=index):
                    value = copy.deepcopy(original)
                    mutate(value)
                    with self.assertRaises(ValueError):
                        validate_manifest_shape(value)

    def test_nested_duplicate_metadata_rejected_before_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, digest = self.candidate(root)
            self.enriched(source)
            path = source / "release.json"
            path.write_text(path.read_text().replace('"k3d": "v5.9.0"', '"k3d": "latest", "k3d": "v5.9.0"'))
            with self.assertRaisesRegex(ValueError, "duplicate release"):
                self.attempt(root, source, digest)
            self.assertFalse((root / "installed/cloudforge").exists())

    def test_reproducibility_ignores_only_two_validated_wall_clock_fields(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, _ = self.candidate(root)
            self.enriched(source)
            second = root / "reproduced"
            shutil.copytree(source, second)
            manifest = json.loads((second / "release.json").read_text())
            manifest.update(qualification_build_started_at="2026-09-22T02:00:00Z", qualification_build_finished_at="2026-09-22T02:00:05Z")
            (second / "release.json").write_text(json.dumps(manifest))
            result = comparison.compare(source, second)
            self.assertEqual(result["result"], "pass")
            self.assertNotEqual(result["canonical_manifest"]["qualification_build_started_at"], result["reproduced_manifest"]["qualification_build_started_at"])
            manifest["date"] = "2026-09-23T00:00:00Z"
            (second / "release.json").write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, "deterministic release metadata differ"):
                comparison.compare(source, second)

    def test_reproducibility_rejects_changed_archive_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, _ = self.candidate(root)
            manifest = self.enriched(source)
            second = root / "reproduced"
            shutil.copytree(source, second)
            archive = second / manifest["archive"]
            archive.write_bytes(archive.read_bytes() + b"changed")
            with self.assertRaisesRegex(ValueError, "archive checksum mismatch"):
                comparison.compare(source, second)

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
