#!/usr/bin/env python3
"""Offline contracts for endpoint policy qualification; no runtime is started."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("endpoint_policy", Path(__file__).with_name("pilot-endpoint-policy.py"))
policy = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(policy)


class EndpointPolicyTests(unittest.TestCase):
    def report(self):
        return {"status": "blocked", "diagnostics": [{"code": "docker_endpoint_unsupported", "status": "blocked"}], "evidence": [{"experiment_id": "container-build", "status": "blocked", "execution": {"executed": False}}]}

    def test_all_declared_native_cases(self):
        for case in policy.CASES:
            trace = [{"kind": "context_metadata"}] if case in ("stored-context", "environment-context") else []
            policy.qualify(self.report(), 1, trace, case)

    def test_rejects_wrong_native_status_exit_or_executed_evidence(self):
        for change in ("status", "exit", "diagnostic", "execution", "privacy"):
            report, code = self.report(), 1
            if change == "status":
                report["status"] = "error"
            elif change == "exit":
                code = 2
            elif change == "diagnostic":
                report["diagnostics"] = []
            elif change == "execution":
                report["evidence"][0]["execution"]["executed"] = True
            else:
                report["diagnostics"][0]["message"] = policy.REMOTE
            with self.assertRaises(AssertionError):
                policy.qualify(report, code, [], "environment-host")

    def test_no_daemon_calls_or_retries_are_permitted(self):
        for trace in ([{"kind": "forbidden_runtime_command"}], [{"kind": "context_metadata"}] * 2):
            with self.assertRaises(AssertionError):
                policy.qualify(self.report(), 1, trace, "stored-context")

    def test_synthetic_context_files_and_selection_precedence(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "config"
            policy.context_configuration(directory)
            metadata = list(directory.glob("contexts/meta/*/meta.json"))
            self.assertEqual(len(metadata), 1)
            self.assertEqual(json.loads(metadata[0].read_text())["Endpoints"]["docker"]["Host"], policy.REMOTE)
            for case in policy.CASES:
                env = policy.selection_environment({"PATH": "/usr/bin", "DOCKER_HOST": "private-before", "DOCKER_CERT_PATH": "private-cert", "KEEP": "ok"}, case, directory, Path(temporary))
                self.assertNotIn("DOCKER_CERT_PATH", env)
                self.assertEqual(env["KEEP"], "ok")
                if case == "environment-context":
                    self.assertEqual(env["DOCKER_CONTEXT"], policy.CONTEXT)
                    self.assertEqual(env["DOCKER_HOST"], "unix:///var/run/docker.sock")
                if case == "default-context-host":
                    self.assertEqual(env["DOCKER_CONTEXT"], "default")
                    self.assertEqual(env["DOCKER_HOST"], policy.REMOTE)

    def test_default_local_execution_is_rejected_before_output_creation(self):
        with tempfile.TemporaryDirectory() as temporary, patch.dict(os.environ, {}, clear=True):
            output = Path(temporary) / "output"
            with patch("sys.argv", ["pilot-endpoint-policy.py", "/unused/binary", str(output)]), self.assertRaises(policy.guards.QualificationError):
                policy.main()
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
