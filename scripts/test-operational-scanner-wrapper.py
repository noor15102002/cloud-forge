#!/usr/bin/env python3
"""Run public scanner fault wrappers with strict env and inert fake executables."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location("operational", Path(__file__).with_name("pilot-operational.py"))
operational = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(operational)
RUNNER = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux"}
IMAGE = "cloudforge/healthy-node-api:" + "a" * 20 + "-a"
IMAGE_ID = "sha256:" + "b" * 64


class OperationalScannerWrapperTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cloudforge-operational-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        shutil.copytree(operational.ROOT / "testdata/healthy-node", self.root / "app")
        (self.root / "bin").mkdir()
        (self.root / "real").mkdir()
        self.real_marker = self.root / "real-trivy.json"
        self.docker_marker = self.root / "real-docker.json"
        self.real_tools = {}
        for tool, marker, output in (("trivy", self.real_marker, "Version: 0.74.0"),
                                     ("docker", self.docker_marker, IMAGE_ID)):
            path = self.root / "real" / tool
            path.write_text("#!" + sys.executable + "\nimport json,os,pathlib,sys\n" +
                            f"pathlib.Path({str(marker)!r}).write_text(json.dumps({{'args':sys.argv[1:],'environment':dict(os.environ)}}))\n" +
                            f"print({output!r})\n")
            path.chmod(0o700)
            self.real_tools[tool] = str(path)
        self.environment = {"PATH": os.environ["PATH"], "DOCKER_HOST": "unix:///var/run/docker.sock",
                            "HOME": str(self.root / "private-scanner-home")}
        self.metadata = operational.create_scanner_metadata(self.root, "scanner-zero", self.real_tools, RUNNER)
        self.wrapper = self.root / "bin/trivy"
        operational.write_tool_wrapper(self.wrapper, "trivy", self.metadata)

    def invoke(self, *arguments):
        return subprocess.run([str(self.wrapper), *arguments], env=self.environment, capture_output=True,
                              text=True, timeout=5, check=False)

    def rewrite_metadata(self, change):
        value = json.loads(self.metadata.read_text())
        change(value)
        self.metadata.write_text(json.dumps(value))

    def assert_rejected(self, result):
        self.assertEqual(result.returncode, 2, (result.stdout, result.stderr))
        self.assertEqual(result.stderr, "Public operational scanner metadata could not be validated.\n")
        self.assertFalse(self.real_marker.exists())
        self.assertFalse(self.docker_marker.exists())
        self.assertFalse((self.root / "injected.json").exists())

    def test_metadata_is_owned_bounded_and_contains_only_confirmed_runner_fields(self):
        self.assertEqual(stat.S_IMODE(self.metadata.stat().st_mode), 0o600)
        value = json.loads(self.metadata.read_text())
        self.assertEqual(value["runner"], RUNNER)
        self.assertLess(self.metadata.stat().st_size, operational.SCANNER_METADATA_LIMIT)
        self.assertEqual(value["source_hashes"], operational.guards.hashes(self.root / "app"))

    def test_real_version_runs_without_restoring_caller_environment(self):
        result = self.invoke("--version")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "Version: 0.74.0\n")
        observed = json.loads(self.real_marker.read_text())
        self.assertEqual(observed["args"], ["--version"])
        self.assertEqual(observed["environment"]["DOCKER_HOST"], self.environment["DOCKER_HOST"])
        self.assertFalse(any(key.startswith(("CF_OPERATIONAL_", "GITHUB_", "TRIVY_")) for key in observed["environment"]))
        self.assertNotIn("RUNNER_ENVIRONMENT", observed["environment"])

    def test_scan_injection_keeps_owned_image_binding_and_pinned_docker_env(self):
        result = self.invoke("image", "--config", str(self.root / "private-config"), IMAGE)
        self.assertEqual(result.returncode, 0, result.stderr)
        observation = json.loads(result.stdout)
        self.assertEqual(observation["ArtifactName"], IMAGE)
        self.assertEqual(observation["Metadata"]["ImageID"], IMAGE_ID)
        observed = json.loads(self.docker_marker.read_text())
        self.assertEqual(observed["args"], ["image", "inspect", "--format", "{{.Id}}", IMAGE])
        self.assertEqual(observed["environment"]["DOCKER_HOST"], self.environment["DOCKER_HOST"])
        self.assertFalse(any(key.startswith(("CF_OPERATIONAL_", "GITHUB_")) for key in observed["environment"]))
        self.assertEqual(json.loads((self.root / "injected.json").read_text())["case"], "scanner-zero")

    def test_non_scanner_case_executes_real_scan_in_controlled_environment(self):
        self.rewrite_metadata(lambda value: value.update(case="cleanup-inventory"))
        result = self.invoke("image", IMAGE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(self.real_marker.read_text())["args"], ["image", IMAGE])
        self.assertFalse(self.docker_marker.exists())
        self.assertFalse((self.root / "injected.json").exists())

    def test_unrelated_image_is_rejected_before_docker_observation(self):
        self.assert_rejected(self.invoke("image", "unrelated/image:tag"))

    def test_invalid_runner_guard_is_rejected_even_with_valid_caller_environment(self):
        self.environment.update(RUNNER)
        self.rewrite_metadata(lambda value: value["runner"].update(RUNNER_ENVIRONMENT="self-hosted"))
        self.assert_rejected(self.invoke("--version"))

    def test_unconfirmed_runner_cannot_create_metadata(self):
        self.metadata.unlink()
        with self.assertRaises(operational.guards.QualificationError):
            operational.create_scanner_metadata(self.root, "scanner-zero", self.real_tools, {})
        self.assertFalse(self.metadata.exists())

    def test_fixture_change_is_rejected(self):
        (self.root / "app/server.js").write_text("changed source")
        self.assert_rejected(self.invoke("--version"))

    def test_nonregular_fixture_cannot_block_wrapper(self):
        os.mkfifo(self.root / "app/blocked")
        self.assert_rejected(self.invoke("--version"))

    def test_bad_metadata_shapes_are_rejected_before_real_tools(self):
        original = self.metadata.read_bytes()
        mutations = [
            lambda value: value.update(schema="unknown"),
            lambda value: value.update(root=str(self.root / "other")),
            lambda value: value.update(case="unknown"),
            lambda value: value.update(unexpected="caller credential"),
            lambda value: value["runner"].update(GITHUB_TOKEN="not allowed"),
            lambda value: value["tools"].update(trivy=str(self.wrapper)),
            lambda value: value["tools"].update(trivy="relative-tool"),
            lambda value: value.update(source_hashes={}),
        ]
        for mutation in mutations:
            self.metadata.write_bytes(original)
            self.rewrite_metadata(mutation)
            self.assert_rejected(self.invoke("--version"))

    def test_duplicate_metadata_fields_are_rejected(self):
        self.metadata.write_text('{"schema":"one","schema":"two"}')
        self.assert_rejected(self.invoke("--version"))

    def test_wrong_permissions_and_oversized_metadata_are_rejected(self):
        self.metadata.chmod(0o644)
        self.assert_rejected(self.invoke("--version"))
        self.metadata.chmod(0o600)
        self.metadata.write_bytes(b"x" * (operational.SCANNER_METADATA_LIMIT + 1))
        self.assert_rejected(self.invoke("--version"))

    def test_metadata_symlink_is_rejected(self):
        target = self.root / "actual.json"
        self.metadata.rename(target)
        self.metadata.symlink_to(target)
        self.assert_rejected(self.invoke("--version"))


if __name__ == "__main__":
    unittest.main()
