#!/usr/bin/env python3
"""Source-level restoration fault guards and outcome assertions; no Docker runs."""
import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("restoration", Path(__file__).with_name("pilot-restoration.py"))
restoration = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(restoration)
RUN = "a" * 20
CONTEXT = "k3d-cloudforge-" + RUN
POD = "cf-broken-shutdown-api-" + RUN + "-123abc-pod12"
PRODUCER = {"version": "v0.1.0-alpha.1", "commit": "b" * 40}
SLOW = ["--context", CONTEXT, "--request-timeout=15s", "get", "--raw",
        "/api/v1/namespaces/cloudforge/pods/" + POD + ":8080/proxy/_test/slow?id=" + RUN + "-shutdown"]
DELETE = ["--context", CONTEXT, "--namespace", "cloudforge", "delete", "pod", POD, "--wait=false"]


def native_report():
    evidence = [
        {"experiment_id": "container-build", "status": "pass"},
        {"experiment_id": "container-scan", "status": "warn"},
        {"experiment_id": "deployment-readiness", "status": "pass"},
        {"experiment_id": "readiness-gating", "status": "skipped", "execution": {"executed": False, "mutation_attempted": False}},
        {"experiment_id": "inflight-shutdown", "status": "fail", "execution": {"executed": True, "mutation_attempted": True},
         "measurements": [{"name": "target_pod", "value": POD}, {"name": "request_id", "value": RUN + "-shutdown"}, {"name": "active_before_delete", "value": "true"}],
         "recovery": {"status": "error", "strategy": "restore_original_deployment_and_validate",
                      "summary": "The intended workload manifest could not be restored.", "checks": []}},
        *[{"experiment_id": name, "status": "blocked", "execution": {"executed": False, "mutation_attempted": False}} for name in restoration.LATER],
        {"experiment_id": "horizontal-autoscaling", "status": "skipped", "execution": {"executed": False, "mutation_attempted": False}},
        {"experiment_id": "environment-cleanup", "status": "pass", "execution": {"executed": True, "mutation_attempted": False}},
    ]
    return {"status": "error", "producer": PRODUCER, "evidence": evidence,
            "findings": [{"id": "runtime.inflight-shutdown", "status": "fail"}],
            "diagnostics": [{"code": "inflight_shutdown_failed", "status": "fail"}]}


class RestorationQualificationTests(unittest.TestCase):
    def root(self):
        temporary = tempfile.TemporaryDirectory(prefix="cloudforge-restoration-")
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name).resolve()
        shutil.copytree(restoration.SOURCE, root / "fixture")
        private = root / "cloudforge-verify-test"
        private.mkdir()
        manifest = private / "workload.yaml"
        manifest.write_text("test generated private workload\n")
        manifest.chmod(0o600)
        return root, ["--context", CONTEXT, "apply", "--filename", str(manifest)]

    def arm(self, root, arguments, slow_exit=1, delete_exit=0):
        identity = restoration.apply_identity(root, arguments)
        restoration.write(root / "baseline.json", identity)
        started = restoration.slow_identity(SLOW)
        restoration.write(root / "slow-started.json", started)
        restoration.write(root / "delete-completed.json", {"target": started, "exit_code": delete_exit})
        restoration.write(root / "slow-completed.json", {"target": started, "exit_code": slow_exit})
        return identity

    def test_fixture_must_be_unchanged_public_source_without_symlinks(self):
        root, _ = self.root()
        self.assertEqual(restoration.validate_fixture(root), restoration.guards.hashes(restoration.SOURCE))
        (root / "fixture/server.js").write_text("arbitrary application")
        with self.assertRaisesRegex(ValueError, "differs"):
            restoration.validate_fixture(root)
        (root / "fixture/server.js").unlink()
        (root / "fixture/server.js").symlink_to(restoration.SOURCE / "server.js")
        with self.assertRaisesRegex(ValueError, "nonregular"):
            restoration.validate_fixture(root)

    def test_apply_requires_same_private_generated_file_and_exact_context(self):
        root, arguments = self.root()
        baseline = restoration.apply_identity(root, arguments)
        self.assertEqual(baseline["run_id"], RUN)
        for changed in [arguments[:1] + ["production"] + arguments[2:], arguments[:4] + [str(root / "fixture/deployment.yaml")]]:
            with self.subTest(arguments=changed), self.assertRaises(ValueError):
                restoration.apply_identity(root, changed)
        Path(arguments[4]).chmod(0o644)
        with self.assertRaises(ValueError):
            restoration.apply_identity(root, arguments)

    def test_only_exact_targeted_slow_request_and_delete_are_recognized(self):
        target = restoration.slow_identity(SLOW)
        self.assertEqual(target["pod"], POD)
        self.assertTrue(restoration.is_target_delete(DELETE, target))
        self.assertFalse(restoration.is_target_delete(DELETE[:-1] + ["--wait=true"], target))
        self.assertFalse(restoration.is_target_delete(DELETE[:6] + ["another-pod", "--wait=false"], target))
        for change in [SLOW[:-1] + [SLOW[-1].replace("/_test/slow", "/_test/state")],
                       SLOW[:-1] + [SLOW[-1].replace("broken-shutdown-api", "healthy-node-api")],
                       [SLOW[0], "k3d-cloudforge-" + "c" * 20, *SLOW[2:]]]:
            self.assertIsNone(restoration.slow_identity(change))

    def test_initial_apply_and_earlier_unarmed_restoration_are_never_injected(self):
        root, arguments = self.root()
        identity = restoration.apply_identity(root, arguments)
        restoration.write(root / "baseline.json", identity)
        self.assertIsNone(restoration.injection_proof(root, identity))
        with patch.object(restoration.guards, "require_runner"), patch.dict(os.environ, {"CF_RESTORATION_ROOT": str(root), "CF_RESTORATION_REAL_KUBECTL": "/trusted/kubectl"}), patch.object(restoration.os, "execv") as execute:
            restoration.tool_wrapper(arguments)
        execute.assert_called_once_with("/trusted/kubectl", ["/trusted/kubectl", *arguments])
        self.assertFalse((root / "injection-observed.json").exists())

    def test_initial_apply_is_executed_and_recorded_without_injection(self):
        root, arguments = self.root()
        with patch.object(restoration.guards, "require_runner"), patch.dict(os.environ, {"CF_RESTORATION_ROOT": str(root), "CF_RESTORATION_REAL_KUBECTL": "/trusted/kubectl"}), patch.object(restoration, "observed_command", return_value=0) as actual:
            self.assertEqual(restoration.tool_wrapper(arguments), 0)
        self.assertEqual(actual.call_args.args[1], arguments)
        self.assertEqual(restoration.read(root / "baseline.json"), restoration.apply_identity(root, arguments))
        self.assertFalse((root / "injection-observed.json").exists())

    def test_injection_requires_real_failed_request_and_successful_target_deletion(self):
        for slow_exit, delete_exit, expected in [(1, 0, True), (0, 0, False), (-15, 0, False), (137, 0, False), (1, 1, False), (True, 0, False)]:
            root, arguments = self.root()
            identity = self.arm(root, arguments, slow_exit, delete_exit)
            with self.subTest(slow_exit=slow_exit, delete_exit=delete_exit):
                proof = restoration.injection_proof(root, identity)
                self.assertEqual(proof is not None, expected)
                if proof:
                    self.assertEqual(proof["injected_exit"], 70)
                    self.assertEqual(proof["original_slow_exit"], 1)
                    self.assertEqual(proof["original_delete_exit"], 0)

    def test_armed_fault_refuses_only_restoration_without_executing_it(self):
        root, arguments = self.root()
        self.arm(root, arguments)
        with patch.object(restoration.guards, "require_runner"), patch.dict(os.environ, {"CF_RESTORATION_ROOT": str(root), "CF_RESTORATION_REAL_KUBECTL": "/trusted/kubectl"}), patch.object(restoration, "observed_command") as actual, patch.object(restoration.os, "execv") as execute, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(restoration.tool_wrapper(arguments), 70)
        actual.assert_not_called()
        execute.assert_not_called()
        self.assertEqual(restoration.read(root / "injection-observed.json")["injected_operation"], "restore_same_owned_workload_apply")

    def test_changed_workload_or_context_cannot_be_injected(self):
        root, arguments = self.root()
        self.arm(root, arguments)
        Path(arguments[4]).write_text("replacement workload")
        with self.assertRaisesRegex(ValueError, "identity changed"):
            restoration.injection_proof(root, restoration.apply_identity(root, arguments))

    def test_runner_guard_refuses_local_execution_before_tools(self):
        with patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}), patch.object(restoration.os, "execv") as execute:
            with self.assertRaises(restoration.guards.QualificationError):
                restoration.tool_wrapper([])
        execute.assert_not_called()

    def test_native_failure_recovery_error_blocking_and_cleanup_are_required(self):
        report = native_report()
        restoration.qualify(report, 2, PRODUCER)
        modifications = [
            lambda r: r.update(status="fail"),
            lambda r: r["producer"].update(version="dev"),
            lambda r: r["evidence"][4].update(status="error"),
            lambda r: r["evidence"][4]["recovery"].update(status="pass"),
            lambda r: r["evidence"][4]["recovery"].update(checks=[{"status": "pass"}]),
            lambda r: r["evidence"][4]["execution"].update(mutation_attempted=False),
            lambda r: r["evidence"][5].update(status="pass"),
            lambda r: r["evidence"][5]["execution"].update(executed=True),
            lambda r: r["evidence"].pop(5),
            lambda r: r["evidence"][-1].update(status="error"),
            lambda r: r["evidence"][-1]["execution"].update(executed=False),
            lambda r: r.update(diagnostics=[]),
            lambda r: r.update(findings=[]),
            lambda r: r["evidence"].append(copy.deepcopy(r["evidence"][4])),
        ]
        for index, modify in enumerate(modifications):
            value = copy.deepcopy(report)
            modify(value)
            with self.subTest(mutation=index), self.assertRaises(AssertionError):
                restoration.qualify(value, 2, PRODUCER)
        with self.assertRaises(AssertionError):
            restoration.qualify(report, 1, PRODUCER)

    def test_native_output_survives_a_failed_qualification_assertion(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            report = native_report()
            report["evidence"][-1]["status"] = "error"
            binary = root / "fake-verifier"
            binary.write_text("#!" + sys.executable + "\nimport sys\nprint(" + repr(json.dumps(report)) + ")\nprint('original native diagnostic', file=sys.stderr)\nsys.exit(2)\n")
            binary.chmod(0o700)
            result = restoration.run_observed([str(binary)], root / "verification.json", timeout=5)
            with self.assertRaises(AssertionError):
                restoration.qualify(json.loads(result.stdout), result.returncode, PRODUCER)
            self.assertEqual(json.loads((root / "verification.json").read_text()), report)
            self.assertEqual((root / "verification.stderr.txt").read_text(), "original native diagnostic\n")
            self.assertEqual(json.loads((root / "verification.exit.json").read_text())["exit_code"], 2)


if __name__ == "__main__":
    unittest.main()
