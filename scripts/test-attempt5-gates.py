#!/usr/bin/env python3
"""Focused scope must retain failures and never waive shared safety contracts."""
import copy
import importlib.util
from pathlib import Path
import re
import tempfile
import unittest

from qualification_record import native_result, read, required_reports, write

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("worker_scope", ROOT / "scripts/attempt5-worker-gate.py")
worker_scope = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker_scope)


class AttemptFive(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.evidence = Path(self.temp.name)
        self.producer = {"version": "dev", "commit": "a" * 40}
        self.record = {
            "schema_version": "qualification-result-v1", "case": "worker-malformed",
            "source_commit": "a" * 40, "expected_producer": self.producer, "binary_sha256": "b" * 64,
            "qualification_status": "FAIL", "native_results": [{"observed_status": "FAIL", "observed_exit_code": 1}],
            "case_assertions": {"status": "FAIL", "binary_unchanged": True, "process_session_settled": True},
            "artifact_preservation": {"full": "COMPLETE", "full_upload_transport": "COMPLETE", "initial_small": "COMPLETE"},
            "independent_cleanup": {
                "schema_version": "qualification-cleanup-v1", "status": "PASS", "completion_observed": True,
                "exit_code": 0, "baseline_checked": True, "started_at": "2026-09-24T00:00:00Z", "finished_at": "2026-09-24T00:00:01Z",
                "resource_classes": {
                    **{kind: {"status": "PASS", "completion_observed": True, "possible_remnants": 0,
                              "observed_identities": [], "identities_omitted": 0, "reason": "complete_inventory_no_matches"}
                       for kind in ("container", "network", "volume", "image", "owned_image", "builder")},
                    **{kind: {"status": "PASS", "completion_observed": True, "reason": "baseline_unchanged"}
                       for kind in ("kubeconfig", "direct_runtime_workspaces")},
                },
            },
        }
        self.native = {"schema_version": "v1alpha8", "run_id": "cf-worker-test", "plan": {"runtime_kind": "worker"},
                       "producer": self.producer, "status": "error", "evidence": [
            *[{"experiment_id": name, "status": "pass"} for name in worker_scope.SHARED_REQUIRED],
            {"experiment_id": "worker-startup", "status": "error", "summary": worker_scope.MALFORMED_SUMMARY,
             "execution": {"executed": True, "mutation_attempted": True},
             "worker": {"samples": [], "key_absent_before_start": True, "pod_uid": "pod-a",
                        "image": "cloudforge/healthy-worker:cf-worker-test-a", "container_restarts": 0}},
            *[{"experiment_id": name, "status": "blocked", "summary": worker_scope.DEPENDENT_SUMMARY,
               "execution": {"executed": False, "mutation_attempted": False}}
              for name in ("worker-recovery", "worker-image-replacement")],
        ]}
        self.store_native()
        write(self.evidence / "qualification.json", {"schema_version": "v1", "case": "malformed",
              "runtime_executed": True, "read_only_plan_passed": True, "qualified": True,
              "cleanup": {"passed": True}, "original_fixture_unchanged": True})
        self.marker = {"schema_version": "worker-heartbeat-observation-v1", "case": "malformed", "run_id": "cf-worker-test",
                       "sequence": 2, "fixture_validated": True, "command_completed": True, "exit_code": 0,
                       "outcome": "malformed_fixture_value", "sensitive_output_retained": False,
                       "started_at": "2026-09-24T00:00:01Z", "finished_at": "2026-09-24T00:00:02Z"}
        write(self.evidence / "worker-heartbeat-observation.json", self.marker)

    def store_native(self):
        write(self.evidence / "stdout.json", self.native)
        write(self.evidence / "exit.json", {"exit_code": 2})
        write(self.evidence / "stdout.observation.json", {
            "started_at": "2026-09-24T00:00:00Z", "finished_at": "2026-09-24T00:00:03Z",
            "completion_observed": True, "exit_code": 2, "observed_status": self.native["status"], "producer": self.producer})
        self.refresh_native_record()

    def refresh_native_record(self):
        self.record["native_results"] = [native_result(self.evidence, required_reports("worker-malformed")[0], self.producer)]

    def assess(self):
        return worker_scope.assess(self.record, self.evidence)

    def test_experimental_failure_is_retained_without_blocking_proven_malformed_scope(self):
        original = copy.deepcopy(self.record)
        result = self.assess()
        self.assertEqual(result["stable_core_gate"], "PASS", result)
        self.assertEqual(result["experimental_qualification"], "FAIL")
        self.assertEqual(result["native_results"][0]["observed_status"], "ERROR")
        self.assertEqual(self.record, original)

    def test_each_shared_runtime_failure_still_blocks(self):
        for name in worker_scope.SHARED_REQUIRED:
            with self.subTest(name=name):
                changed = copy.deepcopy(self.native)
                next(item for item in changed["evidence"] if item["experiment_id"] == name)["status"] = "error"
                write(self.evidence / "stdout.json", changed)
                self.assertEqual(self.assess()["stable_core_gate"], "FAIL")
        write(self.evidence / "stdout.json", self.native)
        self.native["evidence"].append({"experiment_id": "future-shared-prerequisite", "status": "blocked"})
        write(self.evidence / "stdout.json", self.native)
        self.assertEqual(self.assess()["stable_core_gate"], "FAIL")

    def test_missing_cleanup_upload_identity_or_session_boundary_blocks(self):
        original = copy.deepcopy(self.record)
        for fault in ("cleanup", "inventory", "full", "small", "binary", "session", "producer"):
            with self.subTest(fault=fault):
                self.record = copy.deepcopy(original)
                if fault == "cleanup": self.record["independent_cleanup"]["status"] = "UNKNOWN"
                elif fault == "inventory": self.record["independent_cleanup"]["resource_classes"].pop("builder")
                elif fault in ("full", "small"):
                    key = "full_upload_transport" if fault == "full" else "initial_small"
                    self.record["artifact_preservation"][key] = "FAILED"
                elif fault == "binary": self.record["case_assertions"]["binary_unchanged"] = False
                elif fault == "session": self.record["case_assertions"]["process_session_settled"] = False
                else: self.record["source_commit"] = "c" * 40
                self.assertEqual(self.assess()["stable_core_gate"], "FAIL")

    def test_missing_native_remains_incomplete_and_requires_scope_review(self):
        (self.evidence / "stdout.json").unlink()
        (self.evidence / "stdout.observation.json").unlink()
        self.record["qualification_status"] = "INCOMPLETE"
        self.record["artifact_preservation"]["full"] = "PARTIAL"
        result = self.assess()
        self.assertEqual(result["stable_core_gate"], "FAIL")
        self.assertEqual(result["experimental_qualification"], "INCOMPLETE")
        self.assertFalse(result["native_output_available"])
        self.assertIn("worker_failure_scope_unproven_without_native_evidence", result["reason_codes"])

    def test_worker_phase_shared_failures_are_not_exempt(self):
        original = copy.deepcopy(self.native)
        cases = [
            ("worker-startup", "error", "Worker image, identity, replicas or dependencies could not be observed reliably."),
            ("worker-startup", "blocked", "A required provider is not at its intended healthy image."),
            ("worker-startup", "error", "The worker deployment could not be created reliably."),
            ("worker-startup", "error", "Container identity could not be revalidated after heartbeat progress."),
            ("worker-recovery", "error", "Required worker baseline could not be established."),
            ("worker-image-replacement", "error", "Image B could not be imported reliably; worker state was not changed."),
        ]
        for name, status, summary in cases:
            with self.subTest(phase=name, summary=summary):
                self.native = copy.deepcopy(original)
                item = next(item for item in self.native["evidence"] if item["experiment_id"] == name)
                item.update(status=status, summary=summary)
                self.store_native()
                self.assertEqual(self.record["native_results"][0]["contract_status"], "PASS")
                result = self.assess()
                self.assertEqual(result["stable_core_gate"], "FAIL")
                self.assertTrue(any(reason == "worker_failure_scope_unproven" or reason.startswith("unexpected_worker_phase:")
                                    for reason in result["reason_codes"]), result)

    def test_native_exit_timestamp_hash_and_producer_integrity_are_mandatory(self):
        original = copy.deepcopy(self.record)
        for fault in ("missing-exit", "missing-time", "missing-completion", "exit-disagreement", "time-order", "retained-sha", "retained-incomplete", "partial-artifact"):
            with self.subTest(fault=fault):
                self.record = copy.deepcopy(original)
                self.store_native()
                if fault == "missing-exit":
                    (self.evidence / "exit.json").unlink()
                    observation = read(self.evidence / "stdout.observation.json")
                    observation.update(exit_code=None, completion_observed=False)
                    write(self.evidence / "stdout.observation.json", observation)
                elif fault in ("missing-time", "missing-completion", "exit-disagreement", "time-order"):
                    observation = read(self.evidence / "stdout.observation.json")
                    if fault == "missing-time": observation.pop("finished_at")
                    elif fault == "missing-completion": observation["completion_observed"] = False
                    elif fault == "exit-disagreement": observation["exit_code"] = 0
                    else: observation["finished_at"] = "2026-09-23T00:00:00Z"
                    write(self.evidence / "stdout.observation.json", observation)
                if fault in ("missing-exit", "missing-time", "missing-completion", "exit-disagreement", "time-order"):
                    self.refresh_native_record()  # The recomputed record must still be rejected.
                elif fault == "retained-sha": self.record["native_results"][0]["report_sha256"] = "c" * 64
                elif fault == "retained-incomplete":
                    self.record["native_results"][0].update(contract_status="INCOMPLETE", completion_observed=False,
                        observed_exit_code=None, reason_codes=["native_exit_not_observed", "native_timestamps_missing"])
                    self.record["qualification_status"] = "INCOMPLETE"
                else: self.record["artifact_preservation"]["full"] = "PARTIAL"
                self.assertEqual(self.assess()["stable_core_gate"], "FAIL", fault)

    def test_malformed_summary_needs_successful_latest_owned_transport_proof(self):
        marker_path = self.evidence / "worker-heartbeat-observation.json"
        for fault in ("missing", "pending", "failure", "different-value", "wrong-run", "fixture-unvalidated", "stale"):
            with self.subTest(fault=fault):
                marker = copy.deepcopy(self.marker)
                if fault == "missing": marker_path.unlink(missing_ok=True)
                else:
                    if fault == "pending": marker.update(command_completed=False, exit_code=None)
                    elif fault == "failure": marker.update(exit_code=1, outcome="transport_error")
                    elif fault == "different-value": marker["outcome"] = "unexpected_value"
                    elif fault == "wrong-run": marker["run_id"] = "different-run"
                    elif fault == "fixture-unvalidated": marker["fixture_validated"] = False
                    else: marker.update(started_at="2026-09-23T00:00:00Z", finished_at="2026-09-23T00:00:01Z")
                    write(marker_path, marker)
                self.assertEqual(self.assess()["stable_core_gate"], "FAIL", fault)
        write(marker_path, self.marker)
        self.assertEqual(self.assess()["stable_core_gate"], "PASS")

    def test_complete_native_cannot_hide_shared_post_runtime_harness_failure(self):
        marker_path = self.evidence / "qualification.json"
        fixture = read(marker_path)
        fixture.update(qualified=False, failure="worker_report_reload_failed")
        write(marker_path, fixture)
        result = self.assess()
        self.assertEqual(result["stable_core_gate"], "FAIL")
        self.assertIn("malformed_fixture_identity_unproven", result["reason_codes"])

    def test_wrong_case_and_native_identity_cannot_use_experimental_exception(self):
        self.record["case"] = "cancel-build"
        self.assertEqual(self.assess()["stable_core_gate"], "FAIL")
        self.record["case"] = "worker-malformed"
        self.native["producer"] = {"version": "dev", "commit": "c" * 40}
        write(self.evidence / "stdout.json", self.native)
        self.assertEqual(self.assess()["stable_core_gate"], "FAIL")

    def test_focused_case_declares_exactly_the_one_report_it_will_run(self):
        selected = required_reports("reliability-generated")
        self.assertEqual([item["path"] for item in selected], ["generated-1.json"])
        self.assertEqual(selected[0]["expected"], [["pass", 0], ["warn", 0], ["fail", 1]])
        self.assertEqual(len(required_reports("reliability")), 3)
        self.assertEqual(required_reports("operational-cleanup-inventory")[0]["expected_native_cleanup"], ["ERROR"])

    def test_broad_runtime_is_explicit_and_required_checks_stay_automatic(self):
        for name in ("pilot", "reliability", "operational", "worker", "backend", "dependencies", "monorepo"):
            with self.subTest(workflow=name):
                text = (ROOT / ".github/workflows" / (name + ".yml")).read_text()
                self.assertIn("  workflow_dispatch:", text)
                self.assertNotRegex(text, r"(?m)^  (pull_request|push):")
        for name, job in (("ci", "test"), ("integration", "readiness")):
            text = (ROOT / ".github/workflows" / (name + ".yml")).read_text()
            self.assertIn("  pull_request:", text)
            self.assertIn("  " + job + ":", text)

    def test_attempt5_is_opt_in_four_cases_and_only_worker_execution_is_informational(self):
        text = (ROOT / ".github/workflows/pilot.yml").read_text()
        focused = text.split("  attempt5:\n", 1)[1]
        self.assertEqual(re.findall(r"          - case: (.+)", focused), [
            "cancel-build", "reliability-generated", "operational-cleanup-inventory", "worker-malformed"])
        self.assertIn("if: ${{ inputs.scope == 'attempt5' }}", focused)
        self.assertEqual(text.count("if: ${{ inputs.scope != 'attempt5' }}"), 3)
        self.assertNotRegex(focused, r"(?m)^    continue-on-error:")
        self.assertIn("continue-on-error: ${{ matrix.scope == 'experimental' }}", focused)
        self.assertIn('scripts/attempt5-worker-gate.py --record "$RECORDS/final/qualification.json"', focused)
        self.assertIn('--bulk-outcome "$BULK" --small-outcome "$SMALL" --gate', focused)
        self.assertIn('if [ "$FINAL" != success ]', focused)
        self.assertLess(focused.index("--capture-baseline"), focused.index("scripts/qualification-run.py"))


if __name__ == "__main__":
    unittest.main()
