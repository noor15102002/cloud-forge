"""Small qualification records; native outcomes never become harness verdicts."""
import datetime
import hashlib
import json
import os
import re
from pathlib import Path
import signal
import subprocess
import time


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def write(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


MAX_RECORD_BYTES = 16 * 1024 * 1024


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate JSON key")
        value[key] = item
    return value


def reject_constant(value):
    raise ValueError("non-finite JSON number")


def read(path):
    try:
        path = Path(path)
        if path.stat().st_size > MAX_RECORD_BYTES:
            return {}
        value = json.loads(path.read_text(), object_pairs_hook=unique_object, parse_constant=reject_constant)
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError, RecursionError):
        return {}


def observation_started(report):
    path = Path(report).with_suffix(".observation.json")
    if path.exists():
        raise ValueError("native observation already exists; preserve the earlier attempt")
    write(path, {"started_at": now(), "finished_at": None, "completion_observed": False, "exit_code": None})


def observation_finished(report, exit_code):
    path = Path(report).with_suffix(".observation.json")
    value = read(path)
    report_value = read(report)
    value.update(finished_at=now(), completion_observed=isinstance(exit_code, int), exit_code=exit_code,
                 observed_status=report_value.get("status", "UNKNOWN"), producer=report_value.get("producer"))
    write(path, value)


HEALTHY = [["pass", 0], ["warn", 0]]
APPLICATION = HEALTHY + [["fail", 1]]
ERROR = [["error", 2]]
FAIL = [["fail", 1]]


def required_reports(case):
    """Explicit required outputs, declared before any native process is launched."""
    def report(path, expected):
        cleanup = ["PASS"]
        if case == "operational-cleanup-inventory" or case == "cancel-redis-fallback":
            cleanup = ["ERROR"]
        elif case.startswith("cleanup-") and case not in ("cleanup-owned-remnant", "cleanup-already-absent", "cleanup-same-name-unrelated"):
            cleanup = ["ERROR"]
        elif case == "operational-docker-unavailable":
            cleanup = ["PASS", "SKIPPED", "NOT_APPLICABLE"]
        return {"path": path, "expected": expected, "expected_native_cleanup": cleanup}
    if case in ("healthy-node", "healthy-python"):
        broken = ["broken-shutdown", "broken-rollout"] if case == "healthy-node" else ["broken-python-shutdown", "broken-python-readiness"]
        return [report(f"{case}-{i}.json", HEALTHY) for i in range(1, 6)] + [report(f"{name}.json", FAIL) for name in broken]
    if case in ("redis-node", "redis-python"):
        return [report("healthy.json", HEALTHY), report("semantic-degraded.json", FAIL), report("disconnected.json", FAIL), report("dependency-timeout.json", [["blocked", 1]])]
    if case == "external":
        return [report(name + ".json", APPLICATION) for name in ("express", "fastapi")]
    if case == "reliability":
        return [report("source-1.json", FAIL), report("source-2.json", HEALTHY), report("generated-1.json", APPLICATION)]
    if case == "topology":
        return [report(f"replicas-{i}/report.json", APPLICATION) for i in (1, 2)]
    if case == "probe-pacing":
        return [report("default/stdout.json", FAIL), report("interval-2s/stdout.json", HEALTHY)]
    if case == "integration-failures":
        return [report(name + ".json", FAIL) for name in ("broken-shutdown", "broken-rollout")]
    if case == "monorepo-extra":
        return [report("wrong-context.json", FAIL)]
    if case.startswith("action-"):
        return [report("verification.json", HEALTHY)]
    if case == "restoration-failure":
        return [report("verification.json", ERROR)]
    if case.startswith("operational-"):
        name = case.removeprefix("operational-")
        expected = FAIL if name == "startup-failure" else APPLICATION if name in ("scanner-zero", "scanner-findings") else ERROR
        return [report("verification.json", expected)]
    if case.startswith("cleanup-") or case.startswith("backend-cancel-"):
        return [report("stdout.json", ERROR)]
    if case.startswith("cancel-"):
        stage = case.removeprefix("cancel-").removesuffix("-fallback")
        return [report(f"cancel-{stage}.json", ERROR)]
    if case.startswith("backend-"):
        name = case.removeprefix("backend-")
        return [report(f"{name}/stdout.json", APPLICATION if name == "healthy" else [["blocked", 1]] if name == "preparation-failure" else FAIL)]
    if case.startswith("worker-"):
        name = case.removeprefix("worker-")
        expected = HEALTHY if name == "healthy" else ERROR if name in ("future", "malformed", "cancel-startup", "cancel-recovery") else FAIL
        return [report("stdout.json", expected)]
    raise ValueError("unknown qualification case: " + case)


def initialize(case, evidence, records, binary):
    records, binary = Path(records), Path(binary)
    records.mkdir(parents=True, exist_ok=False)
    plan = {"schema_version": "qualification-plan-v1", "case": case, "started_at": now(),
            "evidence_path": str(Path(evidence).resolve()), "required_reports": required_reports(case),
            "source_commit": os.environ.get("GITHUB_SHA"), "run_id": os.environ.get("GITHUB_RUN_ID"),
            "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT"), "workflow": os.environ.get("GITHUB_WORKFLOW"),
            "proof_type": "development-runtime", "proof_category": "C", "proof_categories": ["C"],
            "binary_origin": "source-build", "binary_sha256": None, "producer": None}
    # Persist the complete required plan even when version inspection cannot run.
    write(records / "plan.json", plan)
    identity = subprocess.run([str(binary), "version", "--format", "json"], capture_output=True, timeout=30, check=True)
    version = json.loads(identity.stdout)
    if not isinstance(version.get("version"), str) or not version["version"] or not re.fullmatch(r"[a-f0-9]{40}", version.get("commit", "")):
        raise ValueError("native executable does not expose a full source identity")
    plan["producer"] = {"version": version["version"], "commit": version["commit"]}
    plan["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
    receipt = read(binary.parent / "installation.json")
    if receipt:
        if receipt.get("binary_sha256") != plan["binary_sha256"] or any(receipt.get(key) != plan["producer"][key] for key in ("version", "commit")):
            raise ValueError("installed candidate identity mismatch")
        plan.update(proof_type="installed-candidate-runtime", proof_category="A", proof_categories=["A"], binary_origin="verified-archive", candidate=receipt)
        if plan.get("source_commit") and receipt.get("commit") != plan["source_commit"]:
            raise ValueError("candidate source differs from the workflow revision")
    if case.startswith("action-") and "C" not in plan["proof_categories"]:
        plan["proof_categories"].append("C")
    write(records / "plan.json", plan)
    return plan


def session_groups(session_id):
    # CloudForge's command runner creates new process groups, but keeps the
    # private session created by this supervisor. Never select our own session.
    if session_id <= 1 or session_id == os.getsid(0):
        raise ValueError("refusing to supervise an unrelated session")
    observed = subprocess.run(["ps", "-eo", "sid=,pgid=,stat="], capture_output=True, text=True, timeout=5, check=True)
    groups = set()
    for line in observed.stdout.splitlines():
        session, group, state = line.split()
        if int(session) == session_id and not state.startswith("Z"):
            group_id = int(group)
            if group_id <= 1 or group_id == os.getpgrp():
                raise ValueError("refusing to signal an unrelated process group")
            groups.add(group_id)
    return groups


def active_session(session_id):
    return bool(session_groups(session_id))


def signal_session(process, received):
    for group in session_groups(process.pid):
        try:
            os.killpg(group, received)
        except ProcessLookupError:
            pass


def force_session_closed(process):
    # Re-observe after each signal: a member may create another process group
    # while the session is closing. Only this owned session is ever selected.
    deadline = time.monotonic() + 5
    try:
        while active_session(process.pid):
            signal_session(process, signal.SIGKILL)
            process.poll()
            if time.monotonic() >= deadline:
                break
            time.sleep(0.05)
        process.wait(timeout=15)
        return not active_session(process.pid)
    except (OSError, ValueError, subprocess.SubprocessError):
        # If enumeration is unavailable, at least stop the captured leader;
        # missing session observation can never become a cleanup-ready proof.
        try:
            process.kill()
        except ProcessLookupError:
            pass
        process.wait(timeout=15)
        return False


def settle_session(process, grace):
    deadline = time.monotonic() + grace
    while active_session(process.pid) and time.monotonic() < deadline:
        process.poll()
        time.sleep(min(0.1, max(0, deadline - time.monotonic())))
    if active_session(process.pid):
        return force_session_closed(process)
    process.wait(timeout=15)
    return True


def run_case(command, case, evidence, records, binary, timeout=6600, shutdown_grace=750):
    plan = initialize(case, evidence, records, binary)
    records = Path(records)
    process, timed_out, background = None, False, False
    result = {"started_at": now(), "finished_at": None, "completion_observed": False, "exit_code": None, "status": "UNKNOWN"}
    write(records / "assertions.json", result)
    try:
        with (records / "harness.stdout.txt").open("xb") as stdout, (records / "harness.stderr.txt").open("xb") as stderr:
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr, start_new_session=True)
            try:
                process.wait(timeout=timeout)
            except (subprocess.TimeoutExpired, KeyboardInterrupt):
                timed_out = True
                signal_session(process, signal.SIGINT)
                result["process_session_settled"] = settle_session(process, shutdown_grace)
            if not timed_out:
                background = active_session(process.pid)
                if background:
                    signal_session(process, signal.SIGINT)
                    result["process_session_settled"] = settle_session(process, shutdown_grace)
                else:
                    result["process_session_settled"] = True
    finally:
        if process is not None and result.get("process_session_settled") is not True:
            background = True
            result["process_session_settled"] = force_session_closed(process)
            result["abnormal_session_shutdown"] = True
        code = process.returncode if process else None
        result.update(finished_at=now(), completion_observed=code is not None, exit_code=code,
                      status="INCOMPLETE" if timed_out or background or code is None else "PASS" if code == 0 else "FAIL",
                      outer_timeout=timed_out, unexpected_background_processes=background)
        try:
            result["binary_unchanged"] = hashlib.sha256(Path(binary).read_bytes()).hexdigest() == plan["binary_sha256"]
        except OSError:
            result["binary_unchanged"] = False
        write(records / "assertions.json", result)
        # Available locally immediately after native execution, before workflow cleanup.
        write(records / "native.json", [native_result(Path(evidence), entry, plan["producer"]) for entry in plan["required_reports"]])
    return 1 if timed_out or background or not result["binary_unchanged"] or not result.get("process_session_settled") else code if code is not None else 1


def native_result(evidence, entry, producer):
    # A workflow fallback declares expectations only. Without the initialized
    # plan, no directory or binary has been bound to this native invocation.
    path = evidence / entry["path"] if evidence is not None else None
    report = read(path) if path else {}
    observation = read(path.with_suffix(".observation.json")) if path else {}
    exit_record = read(path.with_suffix(".exit.json")) if path else {}
    if path and path.name == "stdout.json" and not exit_record:
        exit_record = read(path.parent / "exit.json")
    code = exit_record.get("exit_code", observation.get("exit_code"))
    status = report.get("status") if report.get("status") in ("pass", "warn", "fail", "blocked", "error") else None
    complete = isinstance(code, int) and not isinstance(code, bool) and exit_record.get("completion_observed", True)
    available = bool(status and isinstance(report.get("producer"), dict) and isinstance(report.get("evidence"), list)
                     and all(isinstance(item, dict) for item in report["evidence"]))
    matched = available and complete and [status, code] in entry["expected"] and report.get("producer") == producer
    reasons = []
    if not available: reasons.append("native_report_missing_or_unusable")
    if not complete: reasons.append("native_exit_not_observed")
    if available and report.get("producer") != producer: reasons.append("native_producer_mismatch")
    if available and complete and [status, code] not in entry["expected"]: reasons.append("native_outcome_unexpected")
    if not observation.get("started_at") or not observation.get("finished_at"): reasons.append("native_timestamps_missing")
    # Only codes/statuses are retained here; full diagnostics remain in the raw report.
    evidence_rows = report.get("evidence", []) if available else []
    cleanup_rows = [item for item in evidence_rows if item.get("experiment_id") == "environment-cleanup"]
    if len(cleanup_rows) > 1:
        reasons.append("native_cleanup_ambiguous")
    cleanup = cleanup_rows[0].get("status") if len(cleanup_rows) == 1 else None
    cleanup = cleanup.upper() if isinstance(cleanup, str) else "NOT_APPLICABLE" if "NOT_APPLICABLE" in entry.get("expected_native_cleanup", []) and available else "UNKNOWN"
    if cleanup not in entry.get("expected_native_cleanup", ["PASS"]):
        reasons.append("native_cleanup_missing_or_unexpected")
    return {"path": entry["path"], "expected_status_exit": entry["expected"], "observed_status": status.upper() if status else "UNKNOWN",
            "observed_exit_code": code, "completion_observed": bool(complete), "producer": report.get("producer") if available else None,
            "started_at": observation.get("started_at"), "finished_at": observation.get("finished_at"),
            "report_sha256": hashlib.sha256(path.read_bytes()).hexdigest() if path and path.is_file() and path.stat().st_size <= MAX_RECORD_BYTES else None,
            "native_cleanup_status": cleanup, "expected_native_cleanup": entry.get("expected_native_cleanup", ["PASS"]),
            "contract_status": "PASS" if matched and not reasons else "INCOMPLETE" if not available or not complete else "FAIL", "reason_codes": reasons}


def valid_cleanup(cleanup):
    if (cleanup.get("schema_version") != "qualification-cleanup-v1" or cleanup.get("status") != "PASS"
            or cleanup.get("completion_observed") is not True or type(cleanup.get("exit_code")) is not int
            or cleanup["exit_code"] != 0 or cleanup.get("baseline_checked") is not True
            or not cleanup.get("started_at") or not cleanup.get("finished_at")):
        return False
    classes = cleanup.get("resource_classes")
    required = {"container", "network", "volume", "image", "owned_image", "builder", "kubeconfig", "direct_runtime_workspaces"}
    if not isinstance(classes, dict) or not required.issubset(classes):
        return False
    for kind, item in classes.items():
        if not isinstance(item, dict) or item.get("status") != "PASS" or item.get("completion_observed") is not True:
            return False
        if kind in ("kubeconfig", "direct_runtime_workspaces"):
            if item.get("reason") != "baseline_unchanged":
                return False
        elif kind in required:
            if (type(item.get("possible_remnants")) is not int or item["possible_remnants"] != 0
                    or item.get("observed_identities") != [] or type(item.get("identities_omitted")) is not int
                    or item["identities_omitted"] != 0 or item.get("reason") != "complete_inventory_no_matches"):
                return False
    return True


def collect(records, cleanup_path, artifact_state="UNKNOWN", small_state="UNKNOWN", fallback_case=None):
    records = Path(records)
    plan, assertions, cleanup = read(records / "plan.json"), read(records / "assertions.json"), read(cleanup_path)
    plan_observed = bool(plan)
    if not plan_observed:
        # These fields describe the known workflow job, never a native result.
        case = fallback_case or os.environ.get("CLOUDFORGE_QUALIFICATION_CASE")
        try:
            required = required_reports(case) if case else []
        except ValueError:
            required = []
        plan = {"case": case or "UNKNOWN", "required_reports": required,
                "source_commit": os.environ.get("GITHUB_SHA"), "run_id": os.environ.get("GITHUB_RUN_ID"),
                "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT"), "workflow": os.environ.get("GITHUB_WORKFLOW")}
    evidence = Path(plan["evidence_path"]) if plan_observed and plan.get("evidence_path") else None
    native = [native_result(evidence, entry, plan.get("producer")) for entry in plan.get("required_reports", [])]
    cleanup_valid = valid_cleanup(cleanup)
    if not cleanup:
        cleanup = {"status": "UNKNOWN", "completion_observed": False, "reason_codes": ["cleanup_record_missing"]}
    upload_transport = artifact_state
    if artifact_state == "COMPLETE" and (not native or any(item["contract_status"] == "INCOMPLETE" for item in native)):
        artifact_state = "PARTIAL"
    reasons = []
    if not plan_observed or not plan.get("required_reports"): reasons.append("required_case_plan_missing")
    if not native or any(item["contract_status"] != "PASS" for item in native): reasons.append("native_contract_incomplete_or_failed")
    if assertions.get("status") != "PASS" or assertions.get("binary_unchanged") is not True or assertions.get("process_session_settled") is not True: reasons.append("case_assertions_not_passed")
    if not cleanup_valid: reasons.append("independent_cleanup_not_established")
    if artifact_state != "COMPLETE": reasons.append("full_artifact_not_preserved")
    if small_state != "COMPLETE": reasons.append("small_artifact_not_preserved")
    return {"schema_version": "qualification-result-v1", "case": plan.get("case", "UNKNOWN"), "proof_type": plan.get("proof_type", "UNKNOWN"),
            "plan_observed": plan_observed, "expectations_source": "initialized_plan" if plan_observed else "workflow_case_fallback" if native else "UNKNOWN",
            "proof_category": plan.get("proof_category"), "proof_categories": plan.get("proof_categories", []), "binary_origin": plan.get("binary_origin"),
            "run_id": plan.get("run_id"), "run_attempt": plan.get("run_attempt"), "workflow": plan.get("workflow"), "source_commit": plan.get("source_commit"),
            "candidate": plan.get("candidate"), "expected_producer": plan.get("producer"), "binary_sha256": plan.get("binary_sha256"),
            "started_at": plan.get("started_at"), "recorded_at": now(), "native_results": native,
            "case_assertions": assertions, "independent_cleanup": cleanup,
            "artifact_preservation": {"full": artifact_state, "full_upload_transport": upload_transport, "initial_small": small_state},
            "qualification_status": "PASS" if not reasons else "INCOMPLETE" if any(item["contract_status"] == "INCOMPLETE" for item in native) or not native else "FAIL",
            "reason_codes": reasons}


def summary(value):
    lines = ["### Qualification: " + value["case"], "", "| Native report | Expected status / exit | Observed status / exit | Contract |", "|---|---|---|---|"]
    for item in value["native_results"]:
        expected = ", ".join(f"{status.upper()}/{code}" for status, code in item["expected_status_exit"])
        lines.append(f"| {item['path']} | {expected} | {item['observed_status']}/{item['observed_exit_code']} | {item['contract_status']} |")
    lines += ["", f"Case assertions: **{value['case_assertions'].get('status', 'UNKNOWN')}**. Independent cleanup: **{value['independent_cleanup'].get('status', 'UNKNOWN')}**.",
              f"Full artifact: **{value['artifact_preservation']['full']}**. Initial small record: **{value['artifact_preservation']['initial_small']}**. Qualification: **{value['qualification_status']}**.",
              "Missing runner/finalizer evidence remains UNKNOWN; a native expected failure never becomes a native PASS.", ""]
    return "\n".join(lines)
