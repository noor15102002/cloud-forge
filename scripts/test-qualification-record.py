#!/usr/bin/env python3
"""Missing observations and preservation failures must never become green cases."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from qualification_record import collect, initialize, native_result, observation_started, observation_finished, read, required_reports, run_case, write


class QualificationRecords(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.evidence = self.root / "evidence"
        self.evidence.mkdir()
        self.records = self.root / "records"
        self.binary = self.root / "cloudforge"
        self.binary.write_text('#!/usr/bin/env python3\nprint(\'{"version":"dev","commit":"' + 'a' * 40 + '"}\')\n')
        self.binary.chmod(0o700)
        self.producer = {"version": "dev", "commit": "a" * 40}
        self.cleanup = self.root / "cleanup.json"
        write(self.cleanup, {"schema_version": "qualification-cleanup-v1", "status": "PASS", "completion_observed": True,
                             "exit_code": 0, "baseline_checked": True, "started_at": "2026-09-22T00:00:00Z", "finished_at": "2026-09-22T00:00:01Z",
                             "resource_classes": {**{kind: {"status": "PASS", "completion_observed": True, "possible_remnants": 0,
                                                           "observed_identities": [], "identities_omitted": 0, "reason": "complete_inventory_no_matches"}
                                                    for kind in ("container", "network", "volume", "image", "owned_image", "builder")},
                                                  **{kind: {"status": "PASS", "completion_observed": True, "reason": "baseline_unchanged"}
                                                     for kind in ("kubeconfig", "direct_runtime_workspaces")}}})

    def native(self, relative, status="fail", code=1):
        path = self.evidence / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        observation_started(path)
        write(path, {"status": status, "producer": self.producer, "evidence": [{"experiment_id": "environment-cleanup", "status": "pass"}]})
        write(path.with_suffix(".exit.json"), {"exit_code": code, "completion_observed": True})
        observation_finished(path, code)

    def prepare(self, case="monorepo-extra"):
        initialize(case, self.evidence, self.records, self.binary)
        write(self.records / "assertions.json", {"status": "PASS", "binary_unchanged": True, "process_session_settled": True})

    def result(self, bulk="COMPLETE", small="COMPLETE"):
        return collect(self.records, self.cleanup, bulk, small)

    def test_expected_application_failure_remains_native_failure_with_case_pass(self):
        self.prepare()
        self.native("wrong-context.json")
        result = self.result()
        self.assertEqual(result["qualification_status"], "PASS")
        self.assertEqual(result["native_results"][0]["observed_status"], "FAIL")
        self.assertEqual(result["native_results"][0]["observed_exit_code"], 1)

    def test_all_seven_reference_invocations_required_before_execution(self):
        self.prepare("healthy-python")
        self.native("healthy-python-1.json", "warn", 0)
        value = self.result()
        self.assertEqual(len(value["native_results"]), 7)
        self.assertEqual(value["qualification_status"], "INCOMPLETE")
        self.assertEqual(value["native_results"][-1]["observed_status"], "UNKNOWN")

    def test_each_missing_or_conflicting_observation_blocks_qualification(self):
        self.prepare()
        self.native("wrong-context.json")
        path = self.evidence / "wrong-context.json"
        for label in ("missing-exit", "producer", "status", "timestamps", "malformed"):
            with self.subTest(label=label):
                original = {p: p.read_bytes() for p in self.evidence.iterdir()}
                if label == "missing-exit":
                    path.with_suffix(".exit.json").unlink()
                    value = read(path.with_suffix(".observation.json")); value["exit_code"] = None
                    write(path.with_suffix(".observation.json"), value)
                elif label == "producer":
                    value = read(path); value["producer"]["commit"] = "b" * 40; write(path, value)
                elif label == "status":
                    value = read(path); value["status"] = "pass"; write(path, value)
                elif label == "timestamps":
                    path.with_suffix(".observation.json").unlink()
                else:
                    path.write_text("partial original report")
                self.assertNotEqual(self.result()["qualification_status"], "PASS")
                for p, raw in original.items(): p.write_bytes(raw)

    def test_failed_upload_does_not_rewrite_native_or_cleanup(self):
        self.prepare(); self.native("wrong-context.json")
        result = self.result(bulk="FAILED")
        self.assertEqual(result["qualification_status"], "FAIL")
        self.assertEqual(result["case_assertions"]["status"], "PASS")
        self.assertEqual(result["independent_cleanup"]["status"], "PASS")
        self.assertEqual(result["native_results"][0]["observed_status"], "FAIL")
        self.assertEqual(self.result(small="FAILED")["qualification_status"], "FAIL")

    def test_cleanup_missing_unknown_error_or_baseline_missing_blocks(self):
        self.prepare(); self.native("wrong-context.json")
        original = read(self.cleanup)
        for status in ("UNKNOWN", "ERROR", "missing", "baseline-missing"):
            if status == "missing": self.cleanup.unlink()
            else:
                value = copy.deepcopy(original)
                if status == "baseline-missing": value["baseline_checked"] = False
                else: value["status"] = status
                write(self.cleanup, value)
            result = self.result()
            self.assertNotEqual(result["qualification_status"], "PASS")
            self.assertIn("independent_cleanup_not_established", result["reason_codes"])

    def test_cleanup_top_level_pass_cannot_hide_missing_unknown_or_conflicting_classes(self):
        self.prepare(); self.native("wrong-context.json")
        original = read(self.cleanup)
        mutations = ("classes-missing", "class-missing", "class-unknown", "class-incomplete", "remnant-count", "remnant-identities", "omitted", "baseline-missing", "baseline-changed")
        for mutation in mutations:
            value = copy.deepcopy(original)
            if mutation == "classes-missing": value.pop("resource_classes")
            elif mutation == "class-missing": value["resource_classes"].pop("owned_image")
            elif mutation == "class-unknown": value["resource_classes"]["network"]["status"] = "UNKNOWN"
            elif mutation == "class-incomplete": value["resource_classes"]["volume"]["completion_observed"] = False
            elif mutation == "remnant-count": value["resource_classes"]["builder"]["possible_remnants"] = 1
            elif mutation == "remnant-identities": value["resource_classes"]["container"]["observed_identities"] = [{"name": "leftover"}]
            elif mutation == "omitted": value["resource_classes"]["image"]["identities_omitted"] = 1
            elif mutation == "baseline-missing": value["resource_classes"].pop("direct_runtime_workspaces")
            else: value["resource_classes"]["kubeconfig"]["reason"] = "baseline_changed"
            write(self.cleanup, value)
            with self.subTest(mutation=mutation):
                self.assertIn("independent_cleanup_not_established", self.result()["reason_codes"])
                self.assertNotEqual(self.result()["qualification_status"], "PASS")

    def test_candidate_required_cases_are_explicit(self):
        workflow = (Path(__file__).parent.parent / ".github/workflows/release-candidate.yml").read_text()
        for line in workflow.splitlines():
            if line.startswith("          - "):
                self.assertTrue(required_reports(line.strip()[2:]))

    def test_finisher_stages_raw_evidence_and_record_without_prefix_changes(self):
        self.prepare(); self.native("wrong-context.json")
        script = Path(__file__).with_name("finish-qualification.py")
        result = subprocess.run([sys.executable, str(script), "--records", str(self.records), "--cleanup", str(self.cleanup),
                                 "--output", str(self.records / "initial"), "--stage-evidence", str(self.evidence)], capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.evidence / "wrong-context.json").is_file())
        self.assertTrue((self.evidence / "qualification-records/plan.json").is_file())
        self.assertTrue((self.evidence / "independent-cleanup.json").is_file())
        gate = subprocess.run([sys.executable, str(script), "--records", str(self.records), "--cleanup", str(self.cleanup),
                               "--output", str(self.records / "final"), "--bulk-outcome", "failure", "--small-outcome", "success", "--gate"], capture_output=True)
        self.assertEqual(gate.returncode, 1)
        self.assertEqual(read(self.records / "final/qualification.json")["native_results"][0]["observed_status"], "FAIL")

    def test_original_unknown_native_cannot_become_complete_preservation(self):
        self.prepare()
        value = self.result()
        self.assertEqual(value["artifact_preservation"]["full_upload_transport"], "COMPLETE")
        self.assertEqual(value["artifact_preservation"]["full"], "PARTIAL")
        self.assertEqual(value["native_results"][0]["observed_status"], "UNKNOWN")

    def test_missing_plan_retains_workflow_identity_and_expectations_only(self):
        self.native("stdout.json", "warn", 0)
        context = {"CLOUDFORGE_QUALIFICATION_CASE": "worker-healthy", "GITHUB_WORKFLOW": "Worker qualification",
                   "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "4", "GITHUB_SHA": "b" * 40}
        with patch.dict(os.environ, context):
            value = self.result()
        self.assertEqual(value["case"], "worker-healthy")
        self.assertEqual((value["workflow"], value["run_id"], value["run_attempt"], value["source_commit"]),
                         ("Worker qualification", "123", "4", "b" * 40))
        self.assertFalse(value["plan_observed"])
        self.assertEqual(value["expectations_source"], "workflow_case_fallback")
        self.assertEqual(value["qualification_status"], "INCOMPLETE")
        self.assertIn("required_case_plan_missing", value["reason_codes"])
        self.assertEqual(value["artifact_preservation"]["full"], "PARTIAL")
        self.assertEqual(value["artifact_preservation"]["full_upload_transport"], "COMPLETE")
        self.assertIsNone(value["expected_producer"])
        self.assertIsNone(value["binary_sha256"])
        self.assertIsNone(value["started_at"])
        native = value["native_results"][0]
        self.assertEqual(native["expected_status_exit"], [["pass", 0], ["warn", 0]])
        self.assertEqual(native["observed_status"], "UNKNOWN")
        self.assertFalse(native["completion_observed"])
        for field in ("observed_exit_code", "producer", "started_at", "finished_at", "report_sha256"):
            self.assertIsNone(native[field], field)
        self.assertFalse((self.records / "plan.json").exists())

    def test_existing_plan_is_not_replaced_by_fallback_workflow_context(self):
        self.prepare(); self.native("wrong-context.json")
        plan = read(self.records / "plan.json")
        with patch.dict(os.environ, {"CLOUDFORGE_QUALIFICATION_CASE": "worker-healthy", "GITHUB_SHA": "b" * 40}):
            value = self.result()
        self.assertTrue(value["plan_observed"])
        self.assertEqual(value["expectations_source"], "initialized_plan")
        self.assertEqual(value["case"], "monorepo-extra")
        self.assertEqual(value["source_commit"], plan["source_commit"])
        self.assertEqual(value["expected_producer"], self.producer)
        self.assertEqual(value["qualification_status"], "PASS")

    def test_finisher_handles_missing_records_directory_and_still_fails_gate(self):
        script = Path(__file__).with_name("finish-qualification.py")
        output = self.root / "separate-result"
        command = [sys.executable, str(script), "--records", str(self.records), "--cleanup", str(self.cleanup),
                   "--output", str(output), "--stage-evidence", str(self.evidence), "--bulk-outcome", "success", "--small-outcome", "success"]
        env = {**os.environ, "CLOUDFORGE_QUALIFICATION_CASE": "worker-future", "GITHUB_RUN_ID": "321"}
        result = subprocess.run(command, env=env, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        value = read(output / "qualification.json")
        self.assertEqual(value["case"], "worker-future")
        self.assertEqual(value["run_id"], "321")
        self.assertEqual(value["native_results"][0]["expected_status_exit"], [["error", 2]])
        self.assertEqual(value["native_results"][0]["observed_status"], "UNKNOWN")
        self.assertTrue((self.evidence / "independent-cleanup.json").is_file())
        self.assertFalse(self.records.exists())
        gate = subprocess.run(command + ["--gate"], env=env, capture_output=True)
        self.assertEqual(gate.returncode, 1, gate.stderr)
        self.assertEqual(read(output / "qualification.json")["qualification_status"], "INCOMPLETE")

    def test_native_action_exit_record_remains_compatible_with_existing_reader(self):
        report = self.evidence / "verification.json"
        script = Path(__file__).with_name("qualification-observe.py")
        subprocess.run([sys.executable, str(script), "start", str(report)], check=True)
        report.write_text('{"status":"error"}')
        subprocess.run([sys.executable, str(script), "finish", str(report), "2"], check=True)
        self.assertEqual(read(report.with_suffix(".exit.json"))["exit_code"], 2)
        self.assertEqual(read(report.with_suffix(".observation.json"))["observed_status"], "error")

    def test_missing_native_cleanup_or_duplicate_json_cannot_qualify(self):
        self.prepare(); self.native("wrong-context.json")
        path = self.evidence / "wrong-context.json"
        value = read(path); value["evidence"] = []; write(path, value)
        self.assertIn("native_cleanup_missing_or_unexpected", self.result()["native_results"][0]["reason_codes"])
        path.write_text('{"status":"error","status":"fail","producer":' + json.dumps(self.producer) + ',"evidence":[{"experiment_id":"environment-cleanup","status":"pass"}]}')
        self.assertEqual(self.result()["native_results"][0]["observed_status"], "UNKNOWN")
        with path.open("wb") as stream:
            stream.truncate(16 * 1024 * 1024 + 1)
        self.assertEqual(read(path), {})

    def test_outer_deadline_settles_entire_session_including_new_group_child(self):
        import time
        from qualification_record import active_session
        child = "import signal,time; signal.signal(signal.SIGINT,signal.SIG_IGN); time.sleep(20)"
        parent = "import os,signal,subprocess,sys,time; signal.signal(signal.SIGINT,signal.SIG_IGN); subprocess.Popen([sys.executable,'-c'," + repr(child) + "],preexec_fn=os.setpgrp); print(os.getpid(),flush=True); time.sleep(20)"
        start = time.monotonic()
        code = run_case([sys.executable, "-c", parent], "monorepo-extra", self.evidence, self.records, self.binary, timeout=0.2, shutdown_grace=0.2)
        self.assertLess(time.monotonic() - start, 5)
        self.assertNotEqual(code, 0)
        group = int((self.records / "harness.stdout.txt").read_text())
        self.assertFalse(active_session(group))
        value = read(self.records / "assertions.json")
        self.assertTrue(value["process_session_settled"])
        self.assertTrue(value["outer_timeout"])
        self.assertEqual(value["status"], "INCOMPLETE")

    def test_successful_helper_cannot_leave_background_native_work_in_new_group(self):
        from qualification_record import active_session
        child = "import signal,time; signal.signal(signal.SIGINT,signal.SIG_IGN); time.sleep(20)"
        parent = "import os,subprocess,sys; subprocess.Popen([sys.executable,'-c'," + repr(child) + "],preexec_fn=os.setpgrp); print(os.getpid(),flush=True)"
        code = run_case([sys.executable, "-c", parent], "monorepo-extra", self.evidence, self.records, self.binary, timeout=5, shutdown_grace=0.2)
        self.assertEqual(code, 1)
        self.assertFalse(active_session(int((self.records / "harness.stdout.txt").read_text())))
        value = read(self.records / "assertions.json")
        self.assertEqual(value["exit_code"], 0)
        self.assertTrue(value["unexpected_background_processes"])
        self.assertEqual(value["status"], "INCOMPLETE")

    def test_outer_interrupt_retains_actual_native_exit_and_waits_for_completion(self):
        native_report = {"status": "error", "producer": self.producer, "evidence": [{"experiment_id": "environment-cleanup", "status": "pass"}]}
        native = "import signal,time,json; signal.signal(signal.SIGINT,lambda *_:(print(" + repr(json.dumps(native_report)) + ",flush=True),exit(2))); time.sleep(20)"
        scripts = str(Path(__file__).resolve().parent)
        helper = "import sys; sys.path.insert(0," + repr(scripts) + "); from qualification_command import run_observed; run_observed([sys.executable,'-c'," + repr(native) + "]," + repr(str(self.evidence / "cancel-build.json")) + ",timeout=20)"
        code = run_case([sys.executable, "-c", helper], "cancel-build", self.evidence, self.records, self.binary, timeout=1, shutdown_grace=2)
        self.assertEqual(code, 1)
        self.assertEqual(read(self.evidence / "cancel-build.exit.json")["exit_code"], 2)
        self.assertEqual(read(self.evidence / "cancel-build.json"), native_report)
        value = read(self.records / "assertions.json")
        self.assertEqual(value["status"], "INCOMPLETE")
        self.assertTrue(value["process_session_settled"])
        self.assertTrue(read(self.evidence / "cancel-build.observation.json")["completion_observed"])

    def test_action_native_session_preserves_completion_proof_and_actual_exit(self):
        from qualification_command import run_observed
        path = self.evidence / "verification.json"
        native = "import sys; print('original native output'); print('original error',file=sys.stderr); sys.exit(2)"
        result = run_observed([sys.executable, "-c", native], path, timeout=5, own_session=True)
        self.assertEqual(result.returncode, 2)
        record = read(path.with_suffix(".exit.json"))
        self.assertEqual(record["exit_code"], 2)
        self.assertTrue(record["process_session_settled"])
        self.assertTrue(record["completion_observed"])
        self.assertEqual(path.read_text(), "original native output\n")

    def test_group_observer_failure_still_kills_owned_background_processes(self):
        from unittest.mock import patch
        from qualification_record import active_session
        parent = "import os,subprocess,sys; subprocess.Popen([sys.executable,'-c','import time; time.sleep(20)']); print(os.getpid(),flush=True)"
        original_active = active_session
        calls = 0
        def interrupted_observer(session):
            nonlocal calls
            calls += 1
            if calls == 1:
                raise OSError("process observation unavailable")
            return original_active(session)
        with patch("qualification_record.active_session", side_effect=interrupted_observer):
            with self.assertRaises(OSError):
                run_case([sys.executable, "-c", parent], "monorepo-extra", self.evidence, self.records, self.binary, timeout=5, shutdown_grace=0.2)
        self.assertFalse(active_session(int((self.records / "harness.stdout.txt").read_text())))
        value = read(self.records / "assertions.json")
        self.assertEqual(value["status"], "INCOMPLETE")
        self.assertTrue(value["process_session_settled"])
        self.assertTrue(value["abnormal_session_shutdown"])

    def test_harness_failure_keeps_original_streams_and_pending_reports(self):
        code = run_case([sys.executable, "-c", "import sys; print('original out'); print('original err',file=sys.stderr); sys.exit(7)"],
                        "monorepo-extra", self.evidence, self.records, self.binary, timeout=5)
        self.assertEqual(code, 7)
        self.assertEqual((self.records / "harness.stdout.txt").read_text(), "original out\n")
        self.assertEqual((self.records / "harness.stderr.txt").read_text(), "original err\n")
        self.assertEqual(self.result()["qualification_status"], "INCOMPLETE")
        with self.assertRaises(FileExistsError):
            run_case(["false"], "monorepo-extra", self.evidence, self.records, self.binary, timeout=5)

    def test_timeout_retains_incomplete_and_never_marks_case_pass(self):
        code = run_case([sys.executable, "-c", "import time; print('partial',flush=True); time.sleep(20)"],
                        "monorepo-extra", self.evidence, self.records, self.binary, timeout=0.2)
        self.assertNotEqual(code, 0)
        self.assertEqual(read(self.records / "assertions.json")["status"], "INCOMPLETE")
        self.assertEqual((self.records / "harness.stdout.txt").read_text(), "partial\n")


if __name__ == "__main__":
    unittest.main()
