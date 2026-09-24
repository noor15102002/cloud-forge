#!/usr/bin/env python3
"""Prove independent Redis contracts continue only after confirmed cleanup, offline."""
from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from core_fixture import CORE_HEALTHY, EXPERIMENTAL

SCRIPT = Path(__file__).resolve().with_name("pilot-dependencies.py")
SPEC = importlib.util.spec_from_file_location("pilot_dependencies", SCRIPT)
pilot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pilot)


def native_report(name, core=True):
    names = (*CORE_HEALTHY, "container-scan", "dependency.redis", "semantic-readiness")
    evidence = [{"experiment_id": experiment, "status": "pass", "execution": {"executed": True}}
                for experiment in names]
    for item in evidence:
        if item["experiment_id"] in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
            item["execution"]["mutation_attempted"] = True
            item["recovery"] = {"status": "pass", "checks": [{"name": "expected_replicas", "status": "pass"}]}
    evidence += [{"experiment_id": experiment, "status": "skipped", "execution": {"executed": False}}
                 for experiment in (*EXPERIMENTAL, "dependency-loss")]
    if not core:
        for item in evidence:
            if item["experiment_id"] in ("readiness-gating", "inflight-shutdown"):
                item.update(status="pass", execution={"executed": True})
    report = {"schema_version": "v1alpha8", "status": "warn", "evidence": evidence, "diagnostics": [], "findings": [],
              "dependencies": [{"status": "pass", "network_exposure": "cluster-internal", "image": "redis@sha256:fixture"}],
              "fingerprint": {"compatibility_key": "fixture", "dependencies": [{"digest": "sha256:fixture"}]}}
    code = 0
    if name != "healthy":
        report["status"], code = ("blocked" if name == "dependency-timeout" else "fail"), 1
        for item in evidence:
            if name == "dependency-timeout":
                if item["experiment_id"] == "dependency.redis":
                    item["status"] = "fail"
                elif item["experiment_id"] == "deployment-readiness":
                    item["status"] = "blocked"
            elif item["experiment_id"] in ("deployment-readiness", "semantic-readiness"):
                item["status"] = "fail"
                if item["experiment_id"] == "semantic-readiness":
                    item["measurements"] = [{"name": "http_status", "value": "200"},
                                            {"name": "json_status_matched", "value": "false"}]
    return code, report


def cleanup_record():
    classes = {name: {"status": "PASS", "completion_observed": True, "possible_remnants": 0,
                      "observed_identities": [], "identities_omitted": 0, "reason": "complete_inventory_no_matches"}
               for name in ("container", "network", "volume", "image", "owned_image", "builder")}
    classes.update({name: {"status": "PASS", "completion_observed": True, "reason": "baseline_unchanged"}
                    for name in ("kubeconfig", "direct_runtime_workspaces")})
    return {"schema_version": "qualification-cleanup-v1", "status": "PASS", "completion_observed": True,
            "exit_code": 0, "baseline_checked": True, "started_at": "start", "finished_at": "finish",
            "resource_classes": classes}


class DependencyHarnessTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.output = Path(self.directory.name) / "evidence"
        self.observed, self.events, self.original = [], [], {}
        self.stdout, self.stderr = io.StringIO(), io.StringIO()

    def run_harness(self, *, core=True, mutate=None, cleanup=None, interrupt=None, interruption=None, leak=None, render=None, baseline_failure=None):
        def observe(command, path, *, timeout, env):
            name = Path(path).stem
            self.observed.append(name)
            self.events.append((name, "native"))
            self.assertEqual(timeout, 1000)
            self.assertIn(str(Path(env["TMPDIR"]) / "k3d"), str(Path(env["PATH"].split(":")[0]) / "k3d"))
            wrapper = Path(env["TMPDIR"]) / "k3d"
            compile(wrapper.read_text(), str(wrapper), "exec")
            self.assertEqual(env["CF_IMPORT_LOG"], str((self.output / f"{name}.image-import.txt").resolve()))
            code, report = native_report(name, core)
            if mutate:
                code, report = mutate(name, code, report)
            payload = report if isinstance(report, str) else json.dumps(report, indent=2) + "\n"
            path.write_text(payload)
            path.with_suffix(".stderr.txt").write_text("original native stderr\n")
            exit_record = {"exit_code": code, "completion_observed": True, "outer_timeout": name == interrupt}
            path.with_suffix(".exit.json").write_text(json.dumps(exit_record))
            self.original[name] = {p.name: p.read_bytes() for p in (path, path.with_suffix(".stderr.txt"), path.with_suffix(".exit.json"))}
            if name == leak:
                (Path(env["TMPDIR"]) / "cloudforge-verify-leaked").mkdir()
            if name == interrupt:
                raise interruption or subprocess.TimeoutExpired(command, timeout, output=payload)
            return subprocess.CompletedProcess(command, code, payload, "original native stderr\n")

        def run(command, **kwargs):
            self.assertTrue(kwargs["check"])
            self.assertIn(kwargs["timeout"], (30, 60))
            if command[:2] == ["bash", "scripts/pilot-cleanup-check.sh"]:
                if "--capture-baseline" in command:
                    path = Path(command[-1])
                    name = path.name.removesuffix(".cleanup-baseline.json")
                    self.events.append((name, "baseline"))
                    if name == baseline_failure:
                        raise subprocess.CalledProcessError(2, command)
                    value = {"schema_version": "qualification-cleanup-baseline-v1", "status": "PASS"}
                else:
                    path = Path(command[-1])
                    name = path.name.removesuffix(".cleanup.json")
                    self.assertTrue(Path(command[command.index("--baseline") + 1]).is_file())
                    self.events.append((name, "cleanup"))
                    value = cleanup_record()
                    if cleanup:
                        value = cleanup(name, value)
                path.write_text(json.dumps(value))
                return subprocess.CompletedProcess(command, 0, json.dumps(value), "")
            self.assertEqual(command[1], "report")
            name = Path(command[2]).stem
            self.events.append((name, "render"))
            if render:
                render(name, command)
            return subprocess.CompletedProcess(command, 0, Path(command[2]).read_text(), "")

        with patch.object(pilot, "run_observed", side_effect=observe), \
                patch.object(pilot.subprocess, "run", side_effect=run), \
                patch.object(pilot.shutil, "which", side_effect=lambda name: "/unused/" + name), \
                redirect_stdout(self.stdout), redirect_stderr(self.stderr):
            code = pilot.main(["/unused/cloudforge", "healthy-node-redis", str(self.output)] + (["--core"] if core else []))
        summary = json.loads((self.output / "dependency-suite.json").read_text())
        for files in self.original.values():
            for name, payload in files.items():
                self.assertEqual((self.output / name).read_bytes(), payload, "native evidence was changed")
        return code, summary

    def test_all_four_cases_pass_once_for_core_and_original_scope(self):
        for core in (True, False):
            with self.subTest(core=core):
                self.output = Path(self.directory.name) / str(core)
                self.observed.clear()
                self.events.clear()
                self.original.clear()
                code, summary = self.run_harness(core=core)
                self.assertEqual(code, 0)
                self.assertEqual(summary["status"], "PASS")
                self.assertTrue(summary["all_cases_attempted"])
                self.assertEqual(summary["not_started"], [])
                self.assertEqual(self.observed, list(pilot.CASES))
                for index, name in enumerate(pilot.CASES):
                    self.assertLess(self.events.index((name, "cleanup")), self.events.index((name, "render")))
                    if index:
                        self.assertLess(self.events.index((pilot.CASES[index - 1], "cleanup")), self.events.index((name, "native")))

    def test_import_error_without_dependencies_retains_error_and_runs_last_case(self):
        def fail(name, code, report):
            if name == "disconnected":
                report.pop("dependencies")
                report.update(status="error", diagnostics=[{"code": "image_import_failed", "status": "error"}])
                return 2, report
            return code, report
        code, summary = self.run_harness(mutate=fail)
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, list(pilot.CASES))
        self.assertTrue(summary["all_cases_attempted"])
        self.assertIsNone(summary["stopped_reason"])
        self.assertEqual([row["contract_status"] for row in summary["cases"]], ["PASS", "PASS", "FAIL", "PASS"])
        failed = summary["cases"][2]
        self.assertEqual((failed["native_status"], failed["native_exit_code"]), ("error", 2))
        self.assertIn("expected native FAIL/1", failed["failures"][0])
        self.assertIn("disconnected: exit=2; status=error; redis=not_observed", self.stdout.getvalue())

    def test_multiple_assertion_failures_are_aggregated_without_retries(self):
        def fail(name, code, report):
            if name == "healthy":
                report["dependencies"][0]["network_exposure"] = "unexpected"
            elif name == "semantic-degraded":
                next(item for item in report["evidence"] if item["experiment_id"] == "semantic-readiness")["measurements"][0]["value"] = "503"
            return code, report
        code, summary = self.run_harness(mutate=fail)
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, list(pilot.CASES))
        self.assertEqual([row["contract_status"] for row in summary["cases"]], ["FAIL", "FAIL", "PASS", "PASS"])
        self.assertEqual(len([row for row in summary["cases"] if row["failures"]]), 2)

    def test_independent_cleanup_error_unknown_or_incomplete_stops_new_cases(self):
        for mutation in (lambda value: value.update(status="ERROR"), lambda value: value.update(status="UNKNOWN"),
                         lambda value: value.update(completion_observed=False), lambda value: value.update(baseline_checked=False),
                         lambda value: value["resource_classes"].pop("builder")):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                self.output = Path(temporary) / "evidence"
                self.observed.clear()
                self.original.clear()
                def cleanup(name, value):
                    if name == "healthy":
                        mutation(value)
                    return value
                code, summary = self.run_harness(cleanup=cleanup)
                self.assertEqual(code, 1)
                self.assertEqual(self.observed, ["healthy"])
                self.assertEqual(summary["not_started"], list(pilot.CASES[1:]))
                self.assertIn("cleanup", summary["stopped_reason"])

    def test_native_cleanup_failure_stops_even_if_independent_inventory_is_empty(self):
        def fail(name, code, report):
            next(item for item in report["evidence"] if item["experiment_id"] == "environment-cleanup")["status"] = "error"
            return 2, dict(report, status="error")
        code, summary = self.run_harness(mutate=fail)
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertIn("native cleanup", summary["stopped_reason"])
        self.assertTrue(summary["cases"][0]["independent_cleanup"])

    def test_leaked_private_workspace_stops_after_independent_observation(self):
        code, summary = self.run_harness(leak="healthy")
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertIn(("healthy", "cleanup"), self.events)
        self.assertFalse(summary["cases"][0]["private_workspace_cleanup"])
        self.assertIsNotNone(summary["stopped_reason"])

    def test_outer_timeout_keeps_raw_output_observes_cleanup_and_stops(self):
        code, summary = self.run_harness(interrupt="healthy")
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertIn(("healthy", "cleanup"), self.events)
        self.assertIn("TimeoutExpired", summary["stopped_reason"])
        self.assertEqual(summary["cases"][0]["native_status"], "warn")
        self.assertTrue(json.loads((self.output / "healthy.exit.json").read_text())["outer_timeout"])

    def test_native_cancellation_diagnostic_stops_after_cleanup(self):
        def fail(name, code, report):
            return 2, dict(report, status="error", diagnostics=[{"code": "verification_canceled"}])
        code, summary = self.run_harness(mutate=fail)
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertIn("cancellation", summary["stopped_reason"])
        self.assertIn(("healthy", "cleanup"), self.events)

    def test_keyboard_interrupt_stops_scheduling_and_retains_observed_cleanup(self):
        code, summary = self.run_harness(interrupt="healthy", interruption=KeyboardInterrupt())
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertIn(("healthy", "cleanup"), self.events)
        self.assertIn("KeyboardInterrupt", summary["stopped_reason"])

    def test_failed_cleanup_command_stops_scheduling(self):
        def fail_cleanup(name, value):
            raise subprocess.CalledProcessError(2, ["cleanup"])
        code, summary = self.run_harness(cleanup=fail_cleanup)
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertFalse(summary["cases"][0]["independent_cleanup"])

    def test_malformed_native_report_stops_when_cancellation_state_is_unknown(self):
        code, summary = self.run_harness(mutate=lambda name, code, report: (2, "{partial"))
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, ["healthy"])
        self.assertIn("completion/cancellation", summary["stopped_reason"])
        self.assertIn(("healthy", "cleanup"), self.events)

    def test_strict_report_loader_failure_continues_only_after_cleanup(self):
        def render(name, command):
            if name == "healthy":
                raise subprocess.CalledProcessError(2, command)
        code, summary = self.run_harness(render=render)
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, list(pilot.CASES))
        self.assertEqual([row["contract_status"] for row in summary["cases"]], ["FAIL", "PASS", "PASS", "PASS"])
        self.assertIn("CalledProcessError", summary["cases"][0]["failures"][0])

    def test_missing_baseline_stops_before_application_execution(self):
        code, summary = self.run_harness(baseline_failure="healthy")
        self.assertEqual(code, 1)
        self.assertEqual(self.observed, [])
        self.assertIn("cleanup baseline", summary["stopped_reason"])
        self.assertEqual(summary["not_started"], list(pilot.CASES))

    def test_previous_attempt_is_never_overwritten_or_partially_resumed(self):
        code, _ = self.run_harness()
        self.assertEqual(code, 0)
        before = {path.name: path.read_bytes() for path in self.output.iterdir() if path.is_file()}
        self.observed.clear()
        with self.assertRaisesRegex(ValueError, "already exists"):
            self.run_harness()
        self.assertEqual(self.observed, [])
        self.assertEqual({path.name: path.read_bytes() for path in self.output.iterdir() if path.is_file()}, before)

    def test_cli_rejects_private_or_unknown_fixture_before_execution(self):
        with patch.object(pilot, "run_suite") as run, redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                pilot.main(["/unused/cloudforge", "../private-app", str(self.output)])
        self.assertEqual(error.exception.code, 2)
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
