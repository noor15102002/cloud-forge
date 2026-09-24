#!/usr/bin/env python3
"""Keep the experimental worker contract separate from shared release safeguards."""
import argparse
from datetime import datetime
import re
from pathlib import Path
import sys

from qualification_record import native_result, read, required_reports, valid_cleanup, write


# A worker-only result cannot excuse an unavailable image, dependency, network,
# cleanup, producer identity or unpreserved evidence.
SHARED_REQUIRED = {
    "container-build": {"pass"},
    "container-scan": {"pass", "warn"},
    "dependency.redis": {"pass"},
    "network-isolation": {"pass"},
    "environment-cleanup": {"pass"},
}


MALFORMED_SUMMARY = "Worker heartbeat was malformed, clock-uncertain or could not be observed reliably."
DEPENDENT_SUMMARY = "Execution prerequisites were not established before this experiment."


def ordered_timestamps(started, finished):
    try:
        first = datetime.fromisoformat(started.replace("Z", "+00:00"))
        last = datetime.fromisoformat(finished.replace("Z", "+00:00"))
        return first.tzinfo is not None and last.tzinfo is not None and first <= last
    except (ValueError, TypeError, AttributeError):
        return False


def native_integrity(record, evidence, producer):
    """Rebind the final small record to complete, unchanged native evidence."""
    actual = native_result(evidence, required_reports("worker-malformed")[0], producer)
    reasons = ["native_integrity:" + reason for reason in actual["reason_codes"]]
    if record.get("native_results") != [actual]:
        reasons.append("retained_native_result_mismatch")
    observation = read(evidence / "stdout.observation.json")
    exit_record = read(evidence / "stdout.exit.json") or read(evidence / "exit.json")
    code = exit_record.get("exit_code")
    if (type(code) is not int or code != 2 or observation.get("exit_code") != code
            or observation.get("completion_observed") is not True
            or observation.get("observed_status") != "error"
            or observation.get("producer") != producer
            or exit_record.get("completion_observed", True) is not True
            or not ordered_timestamps(observation.get("started_at"), observation.get("finished_at"))):
        reasons.append("native_completion_or_timestamps_unproven")
    if record.get("artifact_preservation", {}).get("full") != "COMPLETE":
        reasons.append("complete_native_artifact_not_preserved")
    return reasons


def known_malformed_scope(native, by_id, evidence):
    """Only this observed fixture payload error is confined to experimental work."""
    reasons = []
    startup = by_id.get("worker-startup", {})
    worker = startup.get("worker", {})
    if (native.get("schema_version") != "v1alpha8" or native.get("status") != "error"
            or native.get("plan", {}).get("runtime_kind") != "worker"
            or startup.get("status") != "error" or startup.get("summary") != MALFORMED_SUMMARY
            or startup.get("execution", {}).get("executed") is not True
            or startup.get("execution", {}).get("mutation_attempted") is not True
            or worker.get("samples") != [] or worker.get("key_absent_before_start") is not True
            or not worker.get("pod_uid") or worker.get("previous_pod_uid")
            or worker.get("image") != "cloudforge/healthy-worker:" + str(native.get("run_id")) + "-a"
            or worker.get("container_restarts") != 0):
        reasons.append("worker_failure_scope_unproven")
    for name in ("worker-recovery", "worker-image-replacement"):
        item = by_id.get(name, {})
        if (item.get("status") != "blocked" or item.get("summary") != DEPENDENT_SUMMARY
                or item.get("execution", {}).get("executed") is not False
                or item.get("execution", {}).get("mutation_attempted", False) is not False
                or item.get("recovery") or item.get("worker")):
            reasons.append("unexpected_worker_phase:" + name)
    for name, item in by_id.items():
        if name not in ("worker-startup", "worker-recovery", "worker-image-replacement"):
            if item.get("status") in ("fail", "error", "blocked"):
                reasons.append("shared_runtime_failure:" + name)
        recovery = item.get("recovery")
        if recovery and recovery.get("status") != "pass":
            reasons.append("recovery_not_established:" + name)
    if any(item.get("status") in ("fail", "error", "blocked") for item in native.get("diagnostics", [])):
        reasons.append("additional_execution_diagnostic")
    # The summary deliberately also covers failed transport. Only a successful
    # exact owned heartbeat command that observed the known fixture payload can
    # distinguish that expected parse error from unavailable Redis/kubectl.
    marker = read(evidence / "worker-heartbeat-observation.json")
    if (marker.get("schema_version") != "worker-heartbeat-observation-v1"
            or marker.get("run_id") != native.get("run_id") or marker.get("case") != "malformed"
            or marker.get("fixture_validated") is not True
            or type(marker.get("sequence")) is not int or marker.get("sequence", 0) < 1
            or not ordered_timestamps(marker.get("started_at"), marker.get("finished_at"))
            or not ordered_timestamps(read(evidence / "stdout.observation.json").get("started_at"), marker.get("started_at"))
            or not ordered_timestamps(marker.get("finished_at"), read(evidence / "stdout.observation.json").get("finished_at"))
            or marker.get("command_completed") is not True or type(marker.get("exit_code")) is not int
            or marker.get("exit_code") != 0 or marker.get("outcome") != "malformed_fixture_value"
            or marker.get("sensitive_output_retained") is not False):
        reasons.append("malformed_payload_transport_proof_missing")
    fixture = read(evidence / "qualification.json")
    if (fixture.get("schema_version") != "v1" or fixture.get("case") != "malformed"
            or fixture.get("runtime_executed") is not True or fixture.get("read_only_plan_passed") is not True
            or fixture.get("qualified") is not True or fixture.get("failure")
            or fixture.get("cleanup", {}).get("passed") is not True
            or fixture.get("original_fixture_unchanged") is not True):
        reasons.append("malformed_fixture_identity_unproven")
    return reasons


def assess(record, evidence):
    reasons = []
    if record.get("schema_version") != "qualification-result-v1" or record.get("case") != "worker-malformed":
        reasons.append("wrong_or_missing_worker_qualification")
    if not valid_cleanup(record.get("independent_cleanup", {})):
        reasons.append("independent_cleanup_not_established")
    artifacts = record.get("artifact_preservation", {})
    # PARTIAL remains PARTIAL when native output is absent, even if all bytes
    # that actually exist were successfully uploaded.
    if artifacts.get("full_upload_transport") != "COMPLETE" or artifacts.get("initial_small") != "COMPLETE":
        reasons.append("attempt_bytes_not_preserved")
    assertions = record.get("case_assertions", {})
    if (assertions.get("binary_unchanged") is not True or assertions.get("process_session_settled") is not True
            or assertions.get("outer_timeout") is True or assertions.get("unexpected_background_processes") is True
            or assertions.get("abnormal_session_shutdown") is True):
        reasons.append("binary_or_process_boundary_unproven")
    producer = record.get("expected_producer", {}) or {}
    commit = record.get("source_commit")
    if (not isinstance(commit, str) or not re.fullmatch(r"[a-f0-9]{40}", commit)
            or producer.get("commit") != commit or not producer.get("version")
            or not re.fullmatch(r"[a-f0-9]{64}", record.get("binary_sha256") or "")):
        reasons.append("producer_identity_unproven")
    native = read(evidence / "stdout.json")
    if native:
        reasons.extend(native_integrity(record, evidence, producer))
        if native.get("producer") != producer:
            reasons.append("native_producer_mismatch")
        items = native.get("evidence")
        if (not isinstance(items, list) or any(not isinstance(item, dict)
                or not isinstance(item.get("experiment_id"), str) for item in items)):
            reasons.append("native_evidence_invalid")
            items = []
        by_id = {item["experiment_id"]: item for item in items}
        if len(by_id) != len(items):
            reasons.append("native_evidence_ambiguous")
        for name, accepted in SHARED_REQUIRED.items():
            if by_id.get(name, {}).get("status") not in accepted:
                reasons.append("shared_prerequisite_not_established:" + name)
        reasons.extend(known_malformed_scope(native, by_id, evidence))
    else:
        # Missing native evidence is not positive proof of a worker-only cause.
        # Preserve INCOMPLETE and request scope review; do not infer a defect.
        reasons.append("worker_failure_scope_unproven_without_native_evidence")
    return {
        "schema_version": "attempt5-worker-scope-v1",
        "case": "worker-malformed",
        "source_commit": commit,
        "experimental_qualification": record.get("qualification_status", "UNKNOWN"),
        "native_results": record.get("native_results", []),
        "native_output_available": bool(native),
        "stable_core_gate": "FAIL" if reasons else "PASS",
        "reason_codes": sorted(set(reasons)),
        "scope": "Only the complete observed malformed-fixture outcome is worker-only; missing or unknown causes require scope review. Shared execution, identity, cleanup and preservation remain required.",
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--record", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--gate", action="store_true")
    args = parser.parse_args()
    value = assess(read(args.record), args.evidence)
    write(args.output, value)
    print("Experimental worker qualification: " + value["experimental_qualification"])
    print("Shared stable-core safeguards: " + value["stable_core_gate"])
    for reason in value["reason_codes"]:
        print(reason)
    return 1 if args.gate and value["stable_core_gate"] != "PASS" else 0


if __name__ == "__main__":
    sys.exit(main())
