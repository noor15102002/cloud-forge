#!/usr/bin/env python3
"""Independent cleanup evidence tests; no Docker or application execution."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("cleanup_observer", Path(__file__).with_name("pilot-cleanup-check.py"))
cleanup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cleanup)


def empty_runner(command, **_):
    return subprocess.CompletedProcess(command, 0, "", "")


class CleanupEvidenceTests(unittest.TestCase):
    def test_shell_entrypoint_forwards_baseline_and_record_paths(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            wrapper = root / "python3"
            wrapper.write_text("#!" + sys.executable + "\nimport json,os,sys\n"
                               "from pathlib import Path\n"
                               "Path(os.environ['CF_CAPTURE_ARGS']).write_text(json.dumps(sys.argv[1:]))\n")
            wrapper.chmod(0o700)
            capture = root / "args.json"
            args = ["--output", str(root / "output.json"), "--baseline", str(root / "baseline.json")]
            result = subprocess.run(["bash", str(Path(__file__).with_name("pilot-cleanup-check.sh")), *args],
                                    env=dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"], CF_CAPTURE_ARGS=str(capture)),
                                    capture_output=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(capture.read_text())[1:], args)

    def test_complete_inventories_include_builders_and_untagged_owned_images(self):
        commands = []

        def runner(command, **kwargs):
            commands.append(command)
            return empty_runner(command, **kwargs)

        classes = cleanup.observe_resources(runner)
        self.assertEqual(set(classes), {"container", "network", "volume", "image", "owned_image", "builder"})
        self.assertTrue(all(item["status"] == "PASS" for item in classes.values()))
        self.assertTrue(any("label=cloudforge.dev/ownership" in command and "--all" in command for command in commands))
        self.assertTrue(any(command[1:3] == ["buildx", "ls"] for command in commands))
        self.assertTrue(all("rm" not in command and "prune" not in command for command in commands))

    def test_dangling_owned_image_cannot_escape_repository_name_filter(self):
        def runner(command, **kwargs):
            output = "sha256:" + "a" * 64 + " <none>:<none>\n" if "label=cloudforge.dev/ownership" in command else ""
            return subprocess.CompletedProcess(command, 0, output, "")

        classes = cleanup.observe_resources(runner)
        self.assertEqual(classes["image"]["status"], "PASS")
        self.assertEqual(classes["owned_image"]["status"], "ERROR")
        self.assertEqual(classes["owned_image"]["possible_remnants"], 1)
        self.assertEqual(classes["owned_image"]["observed_identities"],
                         [{"id": "sha256:" + "a" * 64, "name": "<none>:<none>"}])

    def test_builder_remnant_is_observed_without_deleting_it(self):
        def runner(command, **kwargs):
            output = (json.dumps({"Name": "default", "Nodes": [{"Name": "default"}]}) + "\n" +
                      json.dumps({"Name": "cloudforge-" + "b" * 20, "Nodes": []}) + "\n"
                      if command[1:3] == ["buildx", "ls"] else "")
            return subprocess.CompletedProcess(command, 0, output, "")

        classes = cleanup.observe_resources(runner)
        self.assertEqual(classes["builder"]["status"], "ERROR")
        self.assertEqual(classes["builder"]["possible_remnants"], 1)

    def test_default_builder_and_its_same_named_node_are_one_identity(self):
        # Buildx's ordinary .Name template emits both rows. JSON mode emits
        # only the builder object and nests its nodes (commands/ls.go).
        payload = json.dumps({"Name": "default", "Driver": "docker", "Current": True,
                              "Nodes": [{"Name": "default", "Endpoint": "default"}]}) + "\n"
        self.assertEqual(cleanup.builder_records(payload), ["default"])
        with self.assertRaises(ValueError):
            cleanup.builder_records("default\ndefault\n")

        def runner(command, **kwargs):
            if command[1:3] == ["buildx", "ls"]:
                self.assertEqual(command[-2:], ["--format", "json"])
                return subprocess.CompletedProcess(command, 0, payload, "")
            return empty_runner(command, **kwargs)

        self.assertEqual(cleanup.observe_resources(runner)["builder"]["status"], "PASS")

    def test_malformed_or_duplicate_builder_inventory_stays_unknown(self):
        valid = json.dumps({"Name": "default", "Nodes": []}) + "\n"
        for payload in ("null\n", "{}\n", valid.rstrip(), valid + valid,
                        '{"Name":"default","Name":"hidden","Nodes":[]}\n',
                        '{"Name":"default","Nodes":[],"Err":"unavailable"}\n',
                        '{"Name":"default","Nodes":[{"Err":"unavailable"}]}\n'):
            with self.subTest(payload=payload):
                def runner(command, **kwargs):
                    return subprocess.CompletedProcess(command, 0, payload if command[1:3] == ["buildx", "ls"] else "", "")
                observed = cleanup.observe_resources(runner)["builder"]
                self.assertEqual(observed["status"], "UNKNOWN")
                self.assertFalse(observed["completion_observed"])

    def test_unavailable_inventory_does_not_suppress_other_observations(self):
        def runner(command, **kwargs):
            if command[1] == "container":
                raise subprocess.TimeoutExpired(command, 12)
            output = "k3d-cloudforge-" + "b" * 20 + "-images\n" if command[1] == "volume" else ""
            return subprocess.CompletedProcess(command, 0, output, "")

        classes = cleanup.observe_resources(runner)
        self.assertEqual(len(classes), 6)
        self.assertEqual(classes["container"]["status"], "UNKNOWN")
        self.assertEqual(classes["volume"]["status"], "ERROR")
        status, reasons = cleanup.qualify_cleanup(classes)
        self.assertEqual(status, "ERROR")
        self.assertIn("inventory_unavailable_or_unusable", reasons)
        self.assertIn("possible_run_remnants", reasons)

    def test_exit_zero_partial_inventory_is_unknown(self):
        def runner(command, **kwargs):
            output = "a" * 64 + " k3d-cloudforge-partial" if command[1] == "container" else ""
            return subprocess.CompletedProcess(command, 0, output, "")

        classes = cleanup.observe_resources(runner)
        self.assertEqual(classes["container"]["status"], "UNKNOWN")
        self.assertEqual(cleanup.qualify_cleanup(classes)[0], "UNKNOWN")

    def test_kubeconfig_or_workspace_change_is_not_assumed_clean(self):
        original = {"kubeconfig": {"exists": True, "sha256": "a" * 64, "mode": 384},
                    "direct_runtime_workspaces": {"temporary": []}}
        with tempfile.TemporaryDirectory() as temporary:
            baseline = Path(temporary) / "baseline.json"
            cleanup.write_record(baseline, {"schema_version": "qualification-cleanup-baseline-v1", "status": "PASS", "state": original})
            classes = cleanup.observe_resources(empty_runner)
            self.assertEqual(cleanup.qualify_cleanup(classes, baseline, lambda: original)[0], "PASS")
            for kind in original:
                changed = json.loads(json.dumps(original))
                changed[kind] = {}
                classes = cleanup.observe_resources(empty_runner)
                self.assertEqual(cleanup.qualify_cleanup(classes, baseline, lambda: changed)[0], "ERROR")
                self.assertEqual(classes[kind]["reason"], "baseline_changed")

    def test_missing_baseline_never_creates_private_state_proof(self):
        with tempfile.TemporaryDirectory() as temporary:
            classes = cleanup.observe_resources(empty_runner)
            self.assertEqual(cleanup.qualify_cleanup(classes, Path(temporary) / "absent")[0], "UNKNOWN")
            self.assertFalse(classes["private_state"]["completion_observed"])

    def test_cli_retains_structured_result_and_does_not_overwrite_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "cleanup.json"
            with mock.patch.object(cleanup, "observe_resources", return_value=cleanup.observe_resources(empty_runner)), mock.patch("builtins.print"):
                self.assertEqual(cleanup.main(["--output", str(output)]), 0)
                original = output.read_bytes()
                record = json.loads(original)
                self.assertEqual(record["status"], "PASS")
                self.assertTrue(record["completion_observed"])
                self.assertFalse(record["baseline_checked"])
                self.assertIn("private_state_baseline_not_supplied", record["reason_codes"])
                self.assertTrue(record["started_at"] <= record["finished_at"])
                with self.assertRaises(FileExistsError):
                    cleanup.main(["--output", str(output)])
                self.assertEqual(output.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
