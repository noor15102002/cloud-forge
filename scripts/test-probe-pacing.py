#!/usr/bin/env python3
"""Pure generic fixture and qualification checks; no Docker or Kubernetes."""
import copy
import http.client
from http.server import ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


fixture = load("rate_limited_fixture", ROOT / "testdata/rate-limited-http/server.py")
pilot = load("probe_pacing_pilot", ROOT / "scripts/pilot-probe-pacing.py")


class ProbePacingTests(unittest.TestCase):
    def test_cleanup_failure_retains_bounded_partial_streams_and_state_proofs(self):
        cases = [subprocess.TimeoutExpired(["bash"], 30, output=b"O" * 70000, stderr=b"E" * 70000),
                 OSError("private-spawn-detail")]
        for error in cases:
            with self.subTest(error=type(error).__name__), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                output = root / "output"
                output.mkdir()
                before = {"server.py": "fixed-source-hash"}
                with patch.object(pilot.subprocess, "run", side_effect=error), patch.object(pilot, "public_source", return_value=before):
                    record = pilot.retain_case_cleanup(output, root, before)
                self.assertFalse(record["passed"])
                self.assertFalse(record["completion_observed"])
                self.assertIsNone(record["exit_code"])
                self.assertTrue(record["private_workspace_removed"])
                self.assertTrue(record["source_unchanged"])
                self.assertNotIn("private-spawn-detail", (output / "cleanup.json").read_text())
                if isinstance(error, subprocess.TimeoutExpired):
                    self.assertEqual(record["observer_exit_code"], 124)
                    self.assertEqual((output / "cleanup.stdout.txt").read_bytes(), b"O" * 65536)
                    self.assertEqual((output / "cleanup.stderr.txt").read_bytes(), b"E" * 65536)
                    self.assertTrue(record["stdout_truncated"])
                self.assertEqual(json.loads((output / "cleanup.json").read_text()), record)

    def test_fixed_rolling_limit_and_two_second_spacing(self):
        fast = fixture.RateLimit()
        self.assertTrue(all(fast.allow("client", index / 50) for index in range(60)))
        self.assertFalse(fast.allow("client", 1.2))
        self.assertTrue(fast.allow("another-client", 1.2))
        self.assertTrue(fast.allow("client", 60))
        slow = fixture.RateLimit()
        self.assertTrue(all(slow.allow("client", index * 2) for index in range(120)))

    def test_http_429_is_an_actual_response(self):
        handler = type("FreshHandler", (fixture.Handler,), {"limits": fixture.RateLimit()})
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            codes = []
            for _ in range(61):
                connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=2)
                try:
                    connection.request("GET", "/health")
                    response = connection.getresponse()
                    codes.append(response.status)
                    body = response.read()
                    if response.status == 429:
                        self.assertIn(b"rate_limited", body)
                        self.assertEqual(response.getheader("Retry-After"), "60")
                finally:
                    connection.close()
            self.assertEqual(codes, [200] * 60 + [429])
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def report(self, paced):
        policy = {"probes": {"interval": "2s"}} if paced else {}
        evidence = [{"experiment_id": name, "status": "pass"} for name in ("container-build", "deployment-readiness", "semantic-readiness")]
        for name in pilot.LIFECYCLE:
            evidence.append({"experiment_id": name, "status": "pass", "execution": {"executed": True},
                             "measurements": [], "recovery": {"status": "pass", "checks": [{"status": "pass"}]}})
        return {"schema_version": "v1alpha8", "producer": {"commit": "a" * 40}, "status": "fail",
                "plan": copy.deepcopy(policy), "fingerprint": {"configuration": copy.deepcopy(policy)}, "evidence": evidence}

    def test_default_requires_observed_rate_limit_failure(self):
        report = self.report(False)
        report["evidence"][3]["status"] = "fail"
        with self.assertRaises(pilot.helpers.QualificationError):
            pilot.qualify(report, 1, False)
        report["evidence"][3]["measurements"] = [{"name": "probe_http_status_429", "value": "12"}]
        pilot.qualify(report, 1, False)

    def test_explicit_case_accepts_measured_fail_but_not_429_or_missing_evidence(self):
        report = self.report(True)
        report["evidence"][3]["status"] = "fail"
        pilot.qualify(report, 1, True)
        for mutation in ("429", "blocked", "restoration", "erased-failure", "policy", "execution-error"):
            value = copy.deepcopy(report)
            if mutation == "429":
                value["evidence"][3]["measurements"] = [{"name": "probe_http_status_429", "value": "1"}]
            elif mutation == "blocked":
                value["evidence"][3]["status"] = "blocked"
            elif mutation == "restoration":
                value["evidence"][3]["recovery"]["status"] = "blocked"
            elif mutation == "erased-failure":
                value["status"] = "pass"
            elif mutation == "policy":
                value["fingerprint"]["configuration"] = {}
            else:
                value["status"] = "error"
            with self.subTest(mutation=mutation), self.assertRaises(pilot.helpers.QualificationError):
                pilot.qualify(value, 1, True)

    def test_image_observer_requires_exact_public_owner_image_and_arguments(self):
        owner = "cloudforge-0123abcd"
        arguments = ["buildx", "build", "--builder", owner, "--load", "--provenance=false", "--label",
                     "cloudforge.dev/run-id=" + owner, "--tag", "cloudforge/rate-limited-http:0123abcd-a", "."]
        self.assertEqual(pilot.build_identity(arguments), (owner, arguments[-2]))
        self.assertIsNone(pilot.build_identity(["version"]))
        for value in ([*arguments[:-1], ".."], [*arguments, "--secret", "x"],
                      [*arguments[:-2], "cloudforge/other:0123abcd-a", "."],
                      [*arguments[:7], "cloudforge.dev/run-id=cloudforge-deadbeef", *arguments[8:]]):
            with self.subTest(value=value), self.assertRaises(pilot.helpers.QualificationError):
                pilot.build_identity(value)

    def test_image_observer_preserves_build_streams_and_status_when_retention_fails(self):
        owner = "cloudforge-0123abcd"
        arguments = ["buildx", "build", "--builder", owner, "--load", "--provenance=false", "--label",
                     "cloudforge.dev/run-id=" + owner, "--tag", "cloudforge/rate-limited-http:0123abcd-a", "."]
        for code, fail_retention in ((0, False), (17, False), (0, True)):
            with self.subTest(code=code, fail_retention=fail_retention), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                tool = root / "fake-docker"
                tool.write_text("#!/usr/bin/env python3\nimport json,os,sys\n"
                                "if sys.argv[1:3]==['image','inspect']:\n print(json.dumps(['sha256:'+'a'*64]))\n"
                                "else:\n sys.stdout.buffer.write(b'build-out\\x00\\n');sys.stderr.buffer.write(b'build-err\\x1b[31m\\n');sys.exit(int(os.environ['FAKE_BUILD_EXIT']))\n")
                tool.chmod(0o700)
                output = root / "images"
                if fail_retention:
                    output.write_text("not a directory")
                environment = dict(os.environ, GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted", RUNNER_OS="Linux", FAKE_BUILD_EXIT=str(code))
                environment.update({pilot.PREFIX + "REAL_DOCKER": str(tool), pilot.PREFIX + "IMAGE_OUTPUT": str(output)})
                result = subprocess.run([sys.executable, str(ROOT / "scripts/pilot-probe-pacing.py"), "__docker", *arguments],
                                        cwd=pilot.SOURCE, env=environment, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, code)
                self.assertEqual(result.stdout, b"build-out\x00\n")
                self.assertEqual(result.stderr, b"build-err\x1b[31m\n")
                if code == 0 and not fail_retention:
                    record = json.loads(next(output.glob("*.json")).read_text())
                    self.assertTrue(record["observed"])
                    self.assertEqual(record["layers"], ["sha256:" + "a" * 64])


if __name__ == "__main__":
    unittest.main()
