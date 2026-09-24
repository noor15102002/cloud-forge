#!/usr/bin/env python3
"""Fake-tool checks for topology import observation; no runtime tools or apps."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import reliability_import_observer as observer

OWNER = "cloudforge-" + "a" * 20
IMPORT = ["image", "import", "cloudforge/healthy-node-api:" + "a" * 20 + "-a", "--cluster", OWNER, "--mode", "direct"]
CREATE = ["cluster", "create", OWNER, "--runtime-label", "cloudforge.dev/owned=true@all", "--runtime-label",
          "cloudforge.dev/run-id=" + OWNER + "@all", "--runtime-label", "cloudforge.dev/ownership=" + "b" * 32 + "@all"]
HOSTED = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux"}


def fixture_copy(fixture, case):
    shutil.copytree(observer.ROOT / "testdata/healthy-node", fixture)
    manifest = fixture / "k8s/app.yaml"
    if case == "generated-1":
        shutil.rmtree(fixture / "k8s")
    else:
        manifest.write_text(manifest.read_text().split("---")[0].replace("replicas: 2", "replicas: " + case[-1]).replace(
            "readinessProbe:\n", "readinessProbe:\n            initialDelaySeconds: 8\n"))
    (fixture / "cloudforge.yaml").write_bytes(observer.CONFIG)


class ImportObservationTests(unittest.TestCase):
    @contextmanager
    def setup_case(self, case="generated-1"):
        with tempfile.TemporaryDirectory(prefix="cf-reliability-") as temporary, patch.dict(os.environ, HOSTED):
            root = Path(temporary).resolve()
            fixture = root / "application"
            fixture_copy(fixture, case)
            fake = root / "fake-tool"
            fake.write_text("""#!/usr/bin/env python3
import json,os,signal,sys
from pathlib import Path
with Path(os.environ['FAKE_CALLS']).open('a') as out: out.write(json.dumps(sys.argv[1:])+'\\n')
if os.environ.get('FAKE_SIGNAL') == 'true': os.kill(os.getpid(),signal.SIGTERM)
size=int(os.environ.get('FAKE_BYTES','0'))
sys.stdout.buffer.write(b'O'*size+b'\\x1b[31mpublic-import-out\\x00\\n')
sys.stderr.buffer.write(b'E'*size+b'\\x1b[32mpublic-import-err\\x00\\n')
sys.exit(int(os.environ.get('FAKE_EXIT','0')))
""")
            fake.chmod(0o700)
            output = root / "evidence"
            with patch.object(observer.shutil, "which", return_value=str(fake)), observer.observe_imports(fixture, case, output) as extra:
                environment = dict(os.environ, **extra, FAKE_CALLS=str(root / "calls"))
                yield root, fixture, output, environment

    def invoke(self, environment, arguments):
        return subprocess.run([sys.executable, observer.__file__, *arguments], env=environment, capture_output=True, timeout=10)

    def register(self, environment):
        self.assertEqual(self.invoke(environment, CREATE).returncode, 0)

    def record(self, output):
        records = list(output.glob("*.json"))
        self.assertEqual(len(records), 1)
        return json.loads(records[0].read_text())

    def test_all_exact_public_cases_and_images_are_accepted(self):
        for case in observer.CASES:
            with self.subTest(case=case), self.setup_case(case) as (_, fixture, _, _):
                observer.validate_fixture(fixture, case)
                for suffix in ("a", "b"):
                    arguments = list(IMPORT)
                    arguments[2] = arguments[2][:-1] + suffix
                    observer.validate_import(arguments, OWNER)

    def test_fixture_changes_and_nonregular_inputs_stop_before_tool_execution(self):
        for mutation in ("source", "secret", "symlink", "fifo", "oversized"):
            with self.subTest(mutation=mutation), self.setup_case() as (root, fixture, output, environment):
                self.register(environment)
                (root / "calls").unlink()
                if mutation == "source":
                    (fixture / "server.js").write_text("changed")
                elif mutation == "secret":
                    (fixture / ".env").write_text("SECRET=do-not-publish")
                elif mutation == "symlink":
                    (fixture / "linked").symlink_to(root / "fake-tool")
                elif mutation == "fifo":
                    os.mkfifo(fixture / "pipe")
                else:
                    (fixture / "large").write_bytes(b"x" * (1024 * 1024 + 1))
                result = self.invoke(environment, IMPORT)
                self.assertEqual(result.returncode, 2)
                self.assertNotIn(b"SECRET", result.stderr)
                self.assertFalse((root / "calls").exists())
                self.assertFalse(output.exists())

    def test_unregistered_foreign_or_changed_import_is_never_executed(self):
        variants = [(IMPORT, None), (IMPORT, OWNER.replace("a", "c")),
                    ([*IMPORT, "--extra"], OWNER), ([*IMPORT[:-1], "tools"], OWNER),
                    ([*IMPORT[:2], "private/image:latest", *IMPORT[3:]], OWNER)]
        for arguments, owner in variants:
            with self.subTest(arguments=arguments, owner=owner), self.setup_case() as (root, fixture, output, environment):
                if owner is not None:
                    observer.helpers.write_json(fixture.parent / "import-observer/owner.json", {"run_name": owner})
                self.assertEqual(self.invoke(environment, arguments).returncode, 2)
                self.assertFalse((root / "calls").exists())
                self.assertFalse(output.exists())

    def test_create_requires_product_ownership_and_single_run_registration(self):
        for arguments in (CREATE[:-2], [*CREATE[:-1], "foreign=true@all"]):
            with self.assertRaises(observer.helpers.QualificationError):
                observer.created_run(arguments)
        with self.setup_case() as (root, _, _, environment):
            self.register(environment)
            other = [value.replace("a" * 20, "c" * 20) for value in CREATE]
            self.assertEqual(self.invoke(environment, other).returncode, 2)
            self.assertEqual(len((root / "calls").read_text().splitlines()), 1)

    def test_kubeconfig_and_cluster_commands_pass_through_without_capture(self):
        with self.setup_case() as (root, _, output, environment):
            for arguments in (["kubeconfig", "get", OWNER], ["cluster", "delete", OWNER], CREATE):
                result = self.invoke(dict(environment, FAKE_EXIT="19"), arguments)
                self.assertEqual(result.returncode, 19)
                self.assertEqual(result.stdout, b"\x1b[31mpublic-import-out\x00\n")
            self.assertFalse(output.exists())
            calls = [json.loads(line) for line in (root / "calls").read_text().splitlines()]
            self.assertEqual(calls, [["kubeconfig", "get", OWNER], ["cluster", "delete", OWNER], CREATE])

    def test_failed_import_preserves_exact_arguments_exit_streams_and_bounded_evidence(self):
        with self.setup_case() as (root, _, output, environment):
            self.register(environment)
            result = self.invoke(dict(environment, FAKE_EXIT="17", FAKE_BYTES="200000"), IMPORT)
            self.assertEqual(result.returncode, 17)
            self.assertEqual(result.stdout, b"O" * 200000 + b"\x1b[31mpublic-import-out\x00\n")
            self.assertEqual(result.stderr, b"E" * 200000 + b"\x1b[32mpublic-import-err\x00\n")
            calls = [json.loads(line) for line in (root / "calls").read_text().splitlines()]
            self.assertEqual(calls, [CREATE, IMPORT])
            record = self.record(output)
            self.assertEqual(record["exit_code"], 17)
            self.assertTrue(record["completion_observed"])
            self.assertEqual(record["scope"], "bundled_public_topology_fixture_only")
            self.assertEqual(record["arguments"], IMPORT)
            self.assertEqual(record["native_timeout_seconds"], 180)
            self.assertEqual(record["deadline_owner"], "cloudforge_command_runner")
            self.assertFalse(record["retry_performed"])
            for name in ("stdout", "stderr"):
                retained = record["streams"][name]
                self.assertTrue(retained["truncated"])
                self.assertEqual(retained["retained_bytes"], observer.helpers.IMPORT_STREAM_LIMIT)
                self.assertEqual((output / retained["file"]).read_bytes(), getattr(result, name)[-observer.helpers.IMPORT_STREAM_LIMIT:])

    def test_import_child_signal_remains_a_signal(self):
        with self.setup_case() as (_, _, output, environment):
            self.register(environment)
            result = self.invoke(dict(environment, FAKE_SIGNAL="true"), IMPORT)
            self.assertEqual(result.returncode, -signal.SIGTERM)
            record = self.record(output)
            self.assertEqual(record["exit_code"], -signal.SIGTERM)
            self.assertTrue(record["completion_observed"])

    def test_non_disposable_runner_and_symlink_fixture_are_rejected(self):
        with self.setup_case() as (root, fixture, _, _):
            with patch.dict(os.environ, RUNNER_ENVIRONMENT="self-hosted"), self.assertRaises(observer.helpers.QualificationError):
                observer.validate_fixture(fixture, "generated-1")
            link = root / "application-link"
            link.symlink_to(fixture, target_is_directory=True)
            with self.assertRaises(observer.helpers.QualificationError):
                observer.validate_fixture(link, "generated-1")

    def test_missing_k3d_keeps_product_preflight_responsible(self):
        with tempfile.TemporaryDirectory(prefix="cf-reliability-") as temporary, patch.dict(os.environ, HOSTED):
            fixture = Path(temporary).resolve() / "application"
            fixture_copy(fixture, "generated-1")
            with patch.object(observer.shutil, "which", return_value=None), observer.observe_imports(fixture, "generated-1", fixture.parent / "evidence") as environment:
                self.assertEqual(environment, {})
                self.assertFalse((fixture.parent / "import-observer").exists())


if __name__ == "__main__":
    unittest.main()
