#!/usr/bin/env python3
"""Pure fake-command evidence checks; no worker, Redis, or Kubernetes execution."""
from contextlib import contextmanager
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("pilot_worker", Path(__file__).with_name("pilot-worker.py"))
worker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(worker)
RUN_ID = "a" * 20
ARGUMENTS = ["--context", "k3d-cloudforge-" + RUN_ID, "--namespace", "cloudforge", "exec",
             "deployment/cf-dependency-redis", "--container", "redis", "--", "redis-cli", "-e", "--raw",
             "EVAL", worker.HEARTBEAT_SCRIPT, "1", "cloudforge:worker:heartbeat"]
SNAPSHOT = {"seconds": "1800000000", "microseconds": "2000", "ttl_ms": 2500, "present": True, "value": "{not-valid-json"}


class HeartbeatObservation(unittest.TestCase):
    @contextmanager
    def setup_case(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config, marker = root / "runtime.json", root / "worker-heartbeat-observation.json"
            config.write_text(json.dumps(worker.case_config("malformed")))
            fake = root / "fake-kubectl"
            fake.write_text("""#!/usr/bin/env python3
import json,os,signal,sys
from pathlib import Path
with Path(os.environ['FAKE_CALLS']).open('a') as out: out.write(json.dumps(sys.argv[1:])+'\\n')
sys.stdout.write(os.environ['FAKE_STDOUT']);sys.stdout.flush()
sys.stderr.write('original stderr\\n');sys.stderr.flush()
if os.environ.get('FAKE_SIGNAL') == 'true': os.kill(os.getpid(),signal.SIGTERM)
sys.exit(int(os.environ.get('FAKE_EXIT','0')))
""")
            fake.chmod(0o700)
            environment = dict(os.environ, GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted", RUNNER_OS="Linux",
                               FAKE_STDOUT=json.dumps(SNAPSHOT), FAKE_CALLS=str(root / "calls"))
            environment.update({worker.ENV_PREFIX + "REAL_KUBECTL": str(fake), worker.ENV_PREFIX + "CASE": "malformed",
                                worker.ENV_PREFIX + "CONFIG": str(config), worker.ENV_PREFIX + "HEARTBEAT_OBSERVATION": str(marker),
                                worker.ENV_PREFIX + "SOURCE_HASHES": json.dumps(worker.helpers.hashes(worker.REPOSITORY / "testdata/healthy-worker"))})
            yield root, marker, config, environment

    def invoke(self, environment, arguments=ARGUMENTS):
        return subprocess.run([sys.executable, worker.__file__, "__kubectl", *arguments], env=environment, capture_output=True, timeout=10)

    def test_snapshot_classification_requires_exact_public_malformed_value_and_valid_clock(self):
        self.assertTrue(worker.malformed_snapshot(json.dumps(SNAPSHOT)))
        for field, value in (("value", "private payload"), ("present", False), ("ttl_ms", -1), ("ttl_ms", True),
                             ("ttl_ms", 4000), ("seconds", "0"), ("microseconds", "1000000"), ("oversized", True)):
            with self.subTest(field=field, value=value):
                self.assertFalse(worker.malformed_snapshot(json.dumps(dict(SNAPSHOT, **{field: value}))))
        self.assertFalse(worker.malformed_snapshot('{"present": true, "present": false}'))
        self.assertFalse(worker.malformed_snapshot("not JSON"))
        product = (worker.REPOSITORY / "internal/executor/kubernetes/worker.go").read_text()
        self.assertIn("const heartbeatSnapshotScript = `" + worker.HEARTBEAT_SCRIPT + "`", product)

    def test_real_output_exit_and_arguments_are_unchanged_but_marker_is_boolean_only(self):
        with self.setup_case() as (root, marker, _, environment):
            result = self.invoke(environment)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, environment["FAKE_STDOUT"].encode())
            self.assertEqual(result.stderr, b"original stderr\n")
            self.assertEqual(json.loads((root / "calls").read_text()), ARGUMENTS)
            record = json.loads(marker.read_text())
            self.assertEqual(record["run_id"], RUN_ID)
            self.assertEqual(record["sequence"], 1)
            self.assertTrue(record["command_completed"])
            self.assertEqual(record["exit_code"], 0)
            self.assertEqual(record["outcome"], "malformed_fixture_value")
            self.assertFalse(record["sensitive_output_retained"])
            self.assertTrue(record["fixture_validated"])
            for sensitive in ("{not-valid-json", "cloudforge:worker:heartbeat", "original stderr", "EVAL"):
                self.assertNotIn(sensitive, marker.read_text())
            self.assertLessEqual(record["started_at"], record["finished_at"])

    def test_last_failed_command_replaces_prior_success_and_preserves_failure(self):
        with self.setup_case() as (_, marker, _, environment):
            self.assertEqual(self.invoke(environment).returncode, 0)
            failed = self.invoke(dict(environment, FAKE_EXIT="23"))
            self.assertEqual(failed.returncode, 23)
            record = json.loads(marker.read_text())
            self.assertEqual(record["sequence"], 2)
            self.assertEqual(record["exit_code"], 23)
            self.assertEqual(record["outcome"], "command_failed")
            self.assertTrue(record["command_completed"])

    def test_different_or_oversized_snapshot_never_asserts_malformed_proof(self):
        for output in (json.dumps(dict(SNAPSHOT, value="private payload")), "x" * 100000):
            with self.subTest(size=len(output)), self.setup_case() as (_, marker, _, environment):
                result = self.invoke(dict(environment, FAKE_STDOUT=output))
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout, output.encode())
                self.assertEqual(json.loads(marker.read_text())["outcome"], "different_or_unusable_snapshot")
                self.assertNotIn("private payload", marker.read_text())

    def test_child_signal_remains_signal_and_cannot_reuse_old_success(self):
        with self.setup_case() as (_, marker, _, environment):
            self.assertEqual(self.invoke(environment).returncode, 0)
            result = self.invoke(dict(environment, FAKE_SIGNAL="true"))
            self.assertEqual(result.returncode, -signal.SIGTERM)
            record = json.loads(marker.read_text())
            self.assertEqual(record["sequence"], 2)
            self.assertEqual(record["exit_code"], -signal.SIGTERM)
            self.assertEqual(record["outcome"], "command_failed")

    def test_unobserved_spawn_overwrites_previous_success_with_pending_state(self):
        with self.setup_case() as (_, marker, _, environment):
            self.assertEqual(self.invoke(environment).returncode, 0)
            environment[worker.ENV_PREFIX + "REAL_KUBECTL"] = "/missing-public-qualification-tool"
            self.assertEqual(self.invoke(environment).returncode, 2)
            record = json.loads(marker.read_text())
            self.assertEqual(record["sequence"], 2)
            self.assertFalse(record["command_completed"])
            self.assertIsNone(record["exit_code"])
            self.assertEqual(record["outcome"], "observation_incomplete")

    def test_changed_fixture_configuration_or_command_is_refused_before_execution(self):
        for fault in ("namespace", "key", "script", "container", "configuration", "source_hashes"):
            with self.subTest(fault=fault), self.setup_case() as (root, marker, config, environment):
                arguments = list(ARGUMENTS)
                if fault in ("namespace", "key", "script", "container"):
                    arguments[{"namespace": 3, "key": 15, "script": 13, "container": 7}[fault]] = "unrelated"
                elif fault == "configuration":
                    config.write_text("{}")
                else:
                    environment[worker.ENV_PREFIX + "SOURCE_HASHES"] = "{}"
                self.assertEqual(self.invoke(environment, arguments).returncode, 2)
                self.assertFalse((root / "calls").exists())
                self.assertFalse(marker.exists())

    def test_unrelated_commands_and_other_cases_have_no_marker(self):
        with self.setup_case() as (_, marker, _, environment):
            self.assertEqual(self.invoke(environment, ["version", "--client"]).returncode, 0)
            self.assertFalse(marker.exists())
            environment[worker.ENV_PREFIX + "CASE"] = "healthy"
            self.assertEqual(self.invoke(environment).returncode, 0)
            self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
