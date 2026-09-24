#!/usr/bin/env python3
"""Pure scanner qualification harness tests; no Docker or application execution."""
import base64
import copy
from contextlib import contextmanager
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("scanner_policy_pilot", Path(__file__).with_name("pilot-scanner-policy.py"))
pilot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pilot)


def archive(entries=None):
    buffer = io.BytesIO()
    entries = entries or [("package/package.json", b'{"name":"ip","version":"2.0.1"}'), ("package/lib/ip.js", b"// inert test data\n")]
    with tarfile.open(fileobj=buffer, mode="w:gz") as result:
        for name, data in entries:
            info = tarfile.TarInfo(name)
            info.size = len(data)
            result.addfile(info, io.BytesIO(data))
    return buffer.getvalue()


@contextmanager
def pinned(data):
    with patch.object(pilot, "PACKAGE_SHA256", hashlib.sha256(data).hexdigest()), patch.object(
            pilot, "PACKAGE_INTEGRITY", "sha512-" + base64.b64encode(hashlib.sha512(data).digest()).decode()):
        yield


def raw_scan():
    return {"Metadata": {"ImageID": "sha256:" + "a" * 64}, "Results": [{"Vulnerabilities": [{
        "VulnerabilityID": pilot.ADVISORIES[0], "PkgName": "ip", "InstalledVersion": "2.0.1", "Severity": "HIGH"}]}]}


def native_report():
    evidence = [{"experiment_id": name, "status": "pass", "execution": {"executed": True}}
                for name in pilot.require_core_healthy.__globals__["CORE_HEALTHY"]]
    evidence += [{"experiment_id": name, "status": "skipped", "execution": {"executed": False}}
                 for name in pilot.require_core_healthy.__globals__["EXPERIMENTAL"]]
    evidence += [{"experiment_id": "container-scan", "status": "warn", "execution": {"executed": True},
                  "measurements": [{"name": name, "value": value} for name, value in pilot.POLICY.items()]}]
    for item in evidence:
        if item["experiment_id"] in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
            item["execution"]["mutation_attempted"] = True
            item["recovery"] = {"status": "pass", "checks": [{"status": "pass"}]}
    return {"status": "warn", "evidence": evidence, "fingerprint": {"image_id": "sha256:" + "a" * 64,
            "tools": [{"name": "trivy", "version": "0.74.0"}]},
            "findings": [{"status": "warn", "severity": "high", "observed": pilot.ADVISORIES[0] + " in ip 2.0.1"}]}


class ScannerPolicyPilotTests(unittest.TestCase):
    def test_fixture_adds_only_verified_inert_package_and_copy_instruction(self):
        data = archive()
        with tempfile.TemporaryDirectory() as temporary, pinned(data):
            root = Path(temporary)
            output = root / "output"
            output.mkdir()
            app, selected = pilot.prepare_fixture(root, output, data)
            original = pilot.ROOT / "testdata/healthy-node"
            self.assertEqual((app / "server.js").read_bytes(), (original / "server.js").read_bytes())
            self.assertEqual((app / "package.json").read_bytes(), (original / "package.json").read_bytes())
            self.assertIn("COPY --chown=node:node node_modules/ip/ /app/node_modules/ip/\n", (app / "Dockerfile").read_text())
            record = json.loads((output / "scanner-fixture.json").read_text())
            self.assertTrue(record["application_code_unchanged"])
            self.assertFalse(record["package"]["executed"])
            self.assertEqual(selected, record["effective_hashes"])
            self.assertEqual(record["package"]["sha256"], hashlib.sha256(data).hexdigest())

    def test_package_integrity_failure_precedes_any_extraction(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "package"
            with self.assertRaises(ValueError):
                pilot.extract_package(archive(), target)
            self.assertFalse(target.exists())

    def test_unsafe_duplicate_wrong_package_and_oversized_entries_rejected(self):
        for entries in [
            [("package/../../escape", b"x")], [("/package/escape", b"x")],
            [("package/package.json", b"{}")], [("package/package.json", b'{"name":"other","version":"2.0.1"}')],
            [("package/x", b"1"), ("package/x", b"2")], [("package/large", b"x" * (64 * 1024 + 1))],
        ]:
            data = archive(entries)
            with self.subTest(entries=[name for name, _ in entries]), tempfile.TemporaryDirectory() as temporary, pinned(data), self.assertRaises(ValueError):
                pilot.extract_package(data, Path(temporary) / "package")

    def test_symlink_archive_entry_rejected(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as result:
            item = tarfile.TarInfo("package/link")
            item.type, item.linkname = tarfile.SYMTYPE, "/private"
            result.addfile(item)
        data = buffer.getvalue()
        with tempfile.TemporaryDirectory() as temporary, pinned(data), self.assertRaises(ValueError):
            pilot.extract_package(data, Path(temporary) / "package")

    def test_known_finding_requires_exact_package_version_high_and_unfixed(self):
        self.assertEqual(pilot.require_known_unfixed(raw_scan()), [pilot.ADVISORIES[0]])
        for key, value in [("VulnerabilityID", "other"), ("PkgName", "other"), ("InstalledVersion", "2.0.0"),
                           ("Severity", "CRITICAL"), ("FixedVersion", "2.0.2")]:
            report = raw_scan()
            report["Results"][0]["Vulnerabilities"][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                pilot.require_known_unfixed(report)

    def test_policy_observer_rejects_environment_configuration_ignore_and_endpoint_drift(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = root / "cloudforge-scan-example"
            directory.mkdir()
            config, ignore = directory / "trivy.yaml", directory / ".trivyignore"
            config.write_text("{}\n")
            config.chmod(0o600)
            ignore.write_text("")
            args = ["image", "--config", str(config), "--ignorefile", str(ignore), "--scanners", "vuln",
                    "--severity", pilot.SEVERITIES, "--ignore-unfixed=false", "--image-src", "docker", "--cache-dir", str(directory / "cache")]
            environment = {"HOME": str(directory / "home"), "XDG_CONFIG_HOME": str(directory / "config"),
                           "XDG_CACHE_HOME": str(directory / "cache"), "TMPDIR": str(directory / "temp"),
                           "DOCKER_HOST": "unix:///var/run/docker.sock"}
            state = {"runtime": str(root), "trivy_sha256": "a" * 64}
            with patch.object(pilot.Path, "cwd", return_value=directory), patch.dict(os.environ, environment, clear=True):
                self.assertTrue(pilot.observed_policy(args, state)["ambient_trivy_variables_absent"])
                for injected in ({"TRIVY_FUTURE_POLICY": "hidden"}, {"DOCKER_HOST": "tcp://remote"}, {"HOME": "/private"}):
                    with patch.dict(os.environ, injected), self.assertRaises(ValueError):
                        pilot.observed_policy(args, state)
                config.write_text("severity: [CRITICAL]\n")
                with self.assertRaises(ValueError):
                    pilot.observed_policy(args, state)
                config.write_text("{}\n")
                ignore.write_text(pilot.ADVISORIES[0])
                with self.assertRaises(ValueError):
                    pilot.observed_policy(args, state)

    def test_contract_requires_native_warning_real_identity_and_effective_policy(self):
        summary = pilot.qualify(native_report(), 0, raw_scan())
        self.assertTrue(summary["native_finding_retained"])
        for mutation in ("lost-warning", "policy", "image", "version", "cleanup"):
            report = native_report()
            if mutation == "lost-warning":
                report["findings"] = []
            elif mutation == "policy":
                report["evidence"][-1]["measurements"][0]["value"] = "other"
            elif mutation == "image":
                report["fingerprint"]["image_id"] = "sha256:" + "b" * 64
            elif mutation == "version":
                report["fingerprint"]["tools"][0]["version"] = "0.75.0"
            else:
                next(item for item in report["evidence"] if item["experiment_id"] == "environment-cleanup")["status"] = "error"
            with self.subTest(mutation=mutation), self.assertRaises((ValueError, AssertionError)):
                pilot.qualify(report, 0, raw_scan())

    def test_partial_real_observation_cannot_qualify(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            directory = output / "real-trivy"
            directory.mkdir()
            path = directory / "trivy-scan-123.json"
            stdout = directory / "stdout.txt"
            stdout.write_text(json.dumps(raw_scan()))
            observation = {"exit_code": 0, "completion_observed": True, "actual_command_executed": True,
                           "retry_performed": False, "artifact_write_error": False,
                           "streams": {"stdout": {"file": stdout.name, "truncated": False}}}
            pilot.write_json(path, observation)
            self.assertEqual(pilot.read_real_observation(output)[1], raw_scan())
            for key, value in (("exit_code", 1), ("completion_observed", False), ("actual_command_executed", False), ("retry_performed", True), ("artifact_write_error", True)):
                changed = copy.deepcopy(observation)
                changed[key] = value
                pilot.write_json(path, changed)
                with self.subTest(key=key), self.assertRaises(ValueError):
                    pilot.read_real_observation(output)

    def test_local_execution_rejected_before_output_or_network(self):
        with tempfile.TemporaryDirectory() as temporary, patch.dict(os.environ, {}, clear=True), patch.object(
                sys, "argv", ["pilot", "/missing/binary", str(Path(temporary) / "output")]), patch.object(pilot.urllib.request, "urlopen") as network:
            with self.assertRaises(pilot.guards.QualificationError):
                pilot.main()
            network.assert_not_called()
            self.assertFalse((Path(temporary) / "output").exists())

    def test_original_native_streams_and_exit_survive_harness_failure(self):
        data = archive()
        with tempfile.TemporaryDirectory() as temporary, pinned(data):
            root = Path(temporary)
            binary = root / "fake-cloudforge"
            binary.write_text("#!" + sys.executable + "\nimport sys\nprint('original incomplete native output')\nprint('original native error',file=sys.stderr)\nsys.exit(7)\n")
            binary.chmod(0o700)
            output = root / "output"
            with patch.object(sys, "argv", ["pilot", str(binary), str(output)]), patch.object(
                    pilot.guards, "require_runner"), patch.object(pilot.shutil, "which", return_value=str(binary)), patch.object(
                    pilot.urllib.request, "urlopen", return_value=io.BytesIO(data)), patch.dict(os.environ, {
                        "GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux"}), patch.object(pilot.subprocess, "run", wraps=pilot.subprocess.run) as runtime:
                with self.assertRaises(ValueError):
                    pilot.main()
                self.assertTrue(all(call.args[0][0] == "ps" for call in runtime.call_args_list))
            self.assertEqual((output / "verification.json").read_text(), "original incomplete native output\n")
            self.assertEqual((output / "verification.stderr.txt").read_text(), "original native error\n")
            self.assertEqual(json.loads((output / "verification.exit.json").read_text())["exit_code"], 7)
            self.assertFalse((output / "scanner-policy-result.json").exists())


if __name__ == "__main__":
    unittest.main()
