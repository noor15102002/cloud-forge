#!/usr/bin/env python3
"""Check scope separation without executing an application."""
import copy
import ast
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest.mock import patch
from core_fixture import (CONTROL, CORE_HEALTHY, EXPERIMENTAL, FIXTURES, ROOT, hashes, prepare,
                          require_core_healthy, require_core_scope, require_experimental_skipped)


class CoreProfileTests(unittest.TestCase):
    def test_profiles_change_only_declared_configuration_and_are_repeatable(self):
        for name in sorted(FIXTURES):
            with self.subTest(fixture=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                original = hashes(ROOT / "testdata" / name)
                prepare(name, root / "first", root / "first.json")
                prepare(name, root / "second", root / "second.json")
                self.assertEqual((root / "first.json").read_bytes(), (root / "second.json").read_bytes())
                self.assertEqual(hashes(root / "first"), hashes(root / "second"))
                self.assertEqual(original, hashes(ROOT / "testdata" / name))
                profile = json.loads((root / "first.json").read_text())
                self.assertEqual(profile["configuration_origin"], "explicit_test_configuration")
                self.assertTrue(profile["application_and_build_files_unchanged"])
                for path in (root / "first").rglob("*.yaml"):
                    self.assertNotIn("kind: HorizontalPodAutoscaler", path.read_text())
                self.assertNotIn("control_path", (root / "first/cloudforge.yaml").read_text())
                for file, digest in original.items():
                    if file not in profile["changed_files"]:
                        self.assertEqual(profile["effective_hashes"][file], digest)
                # Compare complete remaining documents, including source
                # replicas, strategy, resources, probes, and Service settings.
                for relative in original:
                    original_bytes = (ROOT / "testdata" / name / relative).read_bytes()
                    actual = (root / "first" / relative).read_bytes()
                    if relative == "cloudforge.yaml":
                        expected = original_bytes.replace(CONTROL.encode(), b"")
                    elif relative in profile["changes"]["removed_hpa_from"]:
                        documents = original_bytes.split(b"\n---\n")
                        expected = b"\n---\n".join(document for document in documents if
                            not document.startswith(b"apiVersion: autoscaling/v2\nkind: HorizontalPodAutoscaler\n"))
                    else:
                        expected = original_bytes
                    self.assertEqual(actual, expected, relative)
                self.assertEqual(set(profile["source_hashes"]), set(profile["effective_hashes"]))

    def test_existing_output_and_unknown_fixture_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in ("../private", "broken-shutdown", "/tmp/app"):
                with self.assertRaises(ValueError):
                    prepare(name, root / "app", root / "record.json")
            prepare("healthy-node", root / "app", root / "record.json")
            with self.assertRaises(ValueError):
                prepare("healthy-node", root / "app", root / "record.json")

    def test_output_must_not_mutate_original_or_become_application_input(self):
        source = ROOT / "testdata/healthy-node"
        before = hashes(source)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for destination, record in ((source / "qualification-copy", root / "record.json"),
                                        (root / "app", source / "qualification-record.json"),
                                        (root / "app", root / "app/record.json")):
                with self.subTest(destination=destination, record=record), self.assertRaises(ValueError):
                    prepare("healthy-node", destination, record)
                self.assertFalse(destination.exists())
                self.assertFalse(record.exists())
        self.assertEqual(hashes(source), before)

    def test_experimental_outcome_cannot_silently_enter_core_profile(self):
        evidence = {key: {"status": "skipped", "execution": {"executed": False}} for key in EXPERIMENTAL}
        require_experimental_skipped(evidence)
        for key in EXPERIMENTAL:
            for status, executed in (("pass", True), ("fail", True), ("skipped", True)):
                changed = copy.deepcopy(evidence)
                changed[key] = {"status": status, "execution": {"executed": executed}}
                with self.subTest(key=key, status=status), self.assertRaises(AssertionError):
                    require_experimental_skipped(changed)
            changed = copy.deepcopy(evidence)
            changed[key]["execution"]["mutation_attempted"] = True
            with self.subTest(key=key, mutation=True), self.assertRaises(AssertionError):
                require_experimental_skipped(changed)

    def healthy_report(self, redis=False):
        names = CORE_HEALTHY + ("container-scan",) + (("dependency.redis", "semantic-readiness") if redis else ())
        evidence = [{"experiment_id": name, "status": "pass", "execution": {"executed": True}} for name in names]
        for item in evidence:
            if item["experiment_id"] in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
                item["execution"]["mutation_attempted"] = True
                item["recovery"] = {"status": "pass", "checks": [{"name": "expected_replicas", "status": "pass"}]}
        evidence += [{"experiment_id": name, "status": "skipped", "execution": {"executed": False}} for name in EXPERIMENTAL]
        return {"status": "pass", "evidence": evidence}

    def test_healthy_core_requires_completed_evidence_and_preserves_warning_policy(self):
        for redis in (False, True):
            report = self.healthy_report(redis)
            require_core_healthy(report, 0, redis)
            report["status"] = "warn"
            next(item for item in report["evidence"] if item["experiment_id"] == "container-scan")["status"] = "warn"
            require_core_healthy(report, 0, redis)
            for name in CORE_HEALTHY + ("container-scan",) + (("dependency.redis", "semantic-readiness") if redis else ()):
                for status, executed in (("fail", True), ("blocked", False), ("error", True), ("skipped", False), ("pass", False)):
                    changed = copy.deepcopy(report)
                    item = next(item for item in changed["evidence"] if item["experiment_id"] == name)
                    item["status"], item["execution"]["executed"] = status, executed
                    with self.subTest(redis=redis, name=name, status=status, executed=executed), self.assertRaises(AssertionError):
                        require_core_healthy(changed, 0, redis)
            for status, code in (("pass", 1), ("fail", 0), ("blocked", 0), ("error", 2)):
                changed = copy.deepcopy(report)
                changed["status"] = status
                with self.subTest(status=status, code=code), self.assertRaises(AssertionError):
                    require_core_healthy(changed, code, redis)

    def test_recovery_and_failed_evidence_cannot_be_erased_by_healthy_overall_status(self):
        report = self.healthy_report()
        for name in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
            for recovery in ({}, {"status": "pass", "checks": []}, {"status": "error", "checks": [{"status": "pass"}]},
                             {"status": "pass", "checks": [{"status": "blocked"}]}):
                changed = copy.deepcopy(report)
                next(item for item in changed["evidence"] if item["experiment_id"] == name)["recovery"] = recovery
                with self.subTest(name=name, recovery=recovery), self.assertRaises(AssertionError):
                    require_core_healthy(changed, 0)
        changed = copy.deepcopy(report)
        changed["evidence"].append({"experiment_id": "unexpected-failure", "status": "fail"})
        with self.assertRaises(AssertionError):
            require_core_healthy(changed, 0)
        changed = copy.deepcopy(report)
        changed["evidence"].append(copy.deepcopy(changed["evidence"][0]))
        with self.assertRaises(AssertionError):
            require_core_healthy(changed, 0)

    def test_fault_core_scope_retains_failure_but_still_requires_cleanup_and_skips(self):
        report = self.healthy_report()
        report["status"] = "fail"
        next(item for item in report["evidence"] if item["experiment_id"] == "rolling-deployment")["status"] = "fail"
        require_core_scope(report)
        self.assertEqual(report["status"], "fail")
        for name in ("environment-cleanup", *EXPERIMENTAL):
            changed = copy.deepcopy(report)
            next(item for item in changed["evidence"] if item["experiment_id"] == name)["status"] = "error"
            with self.subTest(name=name), self.assertRaises(AssertionError):
                require_core_scope(changed)

    def test_embedded_runtime_wrappers_compile_after_indentation(self):
        scripts = ROOT / "scripts"
        tree = ast.parse((scripts / "pilot-dependencies.py").read_text())
        wrappers = []
        for node in ast.walk(tree):
            if (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
                    and isinstance(node.func.value, ast.Name) and node.func.value.id == "textwrap"
                    and node.func.attr == "dedent"):
                wrapper = textwrap.dedent(ast.literal_eval(node.args[0]))
                self.assertEqual(wrapper.splitlines()[0], "#!/usr/bin/env python3")
                wrappers.append(compile(wrapper, "embedded-public-tool-wrapper", "exec"))
        self.assertEqual(len(wrappers), 2, "Both k3d and Redis readiness fault wrappers must be checked")
        for name in ("core_fixture.py", "pilot-fixtures.py", "pilot-dependencies.py", "pilot-integration-failures.py"):
            compile((scripts / name).read_text(), name, "exec")

    def test_core_and_default_reference_selection_keep_separate_contracts(self):
        for core in (False, True):
            observed = []
            def fake_run(command, path, **_kwargs):
                observed.append(Path(path).stem)
                report = self.healthy_report()
                report["fingerprint"] = {"compatibility_key": "fixed-fixture-environment"}
                code = 0
                if not core:
                    for item in report["evidence"]:
                        if item["experiment_id"] in ("readiness-gating", "inflight-shutdown"):
                            item.update(status="pass", execution={"executed": True},
                                        measurements=[{"name": "sigterm_received", "value": "true"}])
                if "broken" in Path(path).stem:
                    code = 1
                    target = "rolling-deployment" if "rollout" in Path(path).stem else "inflight-shutdown"
                    next(item for item in report["evidence"] if item["experiment_id"] == target)["status"] = "fail"
                    report["status"] = "fail"
                return subprocess.CompletedProcess(command, code, json.dumps(report), "")
            with self.subTest(core=core), tempfile.TemporaryDirectory() as temporary:
                script = ROOT / "scripts/pilot-fixtures.py"
                arguments = [str(script), "/fake/cloudforge", "healthy-node", str(Path(temporary) / "output")]
                with patch.object(sys, "argv", arguments + (["--core"] if core else [])), \
                        patch("qualification_command.run_observed", side_effect=fake_run), \
                        patch("subprocess.check_output", return_value=""), redirect_stdout(io.StringIO()):
                    if core:
                        with self.assertRaises(SystemExit) as ended:
                            runpy.run_path(str(script), run_name="__main__")
                        self.assertEqual(ended.exception.code, 0)
                    else:
                        runpy.run_path(str(script), run_name="__main__")
                expected = [f"healthy-node-{number}" for number in range(1, 6)]
                self.assertEqual(observed, expected if core else expected + ["broken-shutdown", "broken-rollout"])

    def test_core_rollout_fault_keeps_default_shutdown_case_in_original_suite(self):
        for core in (False, True):
            observed = []
            def fake_run(command, path, **_kwargs):
                name = Path(path).stem
                observed.append(name)
                report = self.healthy_report()
                report.update(status="fail", findings=[{"id": "runtime.inflight-shutdown", "status": "fail"}],
                              diagnostics=[{"code": "rolling_deployment_failed", "status": "fail"}])
                target = "rolling-deployment" if name == "broken-rollout" else "inflight-shutdown"
                next(item for item in report["evidence"] if item["experiment_id"] == target).update(
                    status="fail", execution={"executed": True}, measurements=[{"name": "target_ready_pods", "value": "0"}])
                return subprocess.CompletedProcess(command, 1, json.dumps(report), "")
            with self.subTest(core=core), tempfile.TemporaryDirectory() as temporary:
                script = ROOT / "scripts/pilot-integration-failures.py"
                arguments = [str(script), "/fake/cloudforge", str(Path(temporary) / "output")]
                with patch.object(sys, "argv", arguments + (["--core"] if core else [])), \
                        patch("qualification_command.run_observed", side_effect=fake_run):
                    runpy.run_path(str(script), run_name="__main__")
                self.assertEqual(observed, ["broken-rollout"] if core else ["broken-shutdown", "broken-rollout"])


if __name__ == "__main__":
    unittest.main()
