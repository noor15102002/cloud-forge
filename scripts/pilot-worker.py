#!/usr/bin/env python3
"""Qualify bounded Redis-heartbeat workers while retaining exact CLI evidence.

Default execution is read-only analyze/plan. --run requires disposable hosted
Linux. Runtime cases never claim queue job completion or business correctness.
"""
import argparse
import copy
from datetime import datetime
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest


REPOSITORY = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("backend_cancellation_helpers", REPOSITORY / "scripts/pilot-backend-cancellation.py")
helpers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helpers)
CASES = ("healthy", "never", "stale", "frozen", "future", "malformed", "exit", "first-pod-only", "fail-second-start", "cancel-startup", "cancel-recovery")
WORKER_IDS = ("worker-startup", "worker-recovery", "worker-image-replacement")
HTTP_IDS = ("deployment-readiness", "semantic-readiness", "graceful-shutdown", "pod-recovery", "rolling-deployment", "load-profile", "horizontal-autoscaling", "readiness-gating", "inflight-shutdown")
ENV_PREFIX = "CF_WORKER_QUALIFY_"
VERIFY_SECONDS = 2400
CLEANUP_SECONDS = 240


def save(path, value):
    pending = path.with_suffix(path.suffix + ".pending")
    helpers.write_json(pending, value)
    pending.replace(path)


def read_state(path):
    return json.loads(path.read_text()) if path.exists() else {"pods": [], "observer_errors": []}


def safe_controller_object(value, run_id, kind):
    identity = helpers.metadata(value, run_id, role="application")
    if not identity:
        return None
    labels = {"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": run_id, "cloudforge.dev/role": "application"}
    result = {"metadata": dict(identity, namespace="cloudforge", labels=labels)}
    if kind == "ReplicaSet":
        owner = helpers.controller(value, "Deployment")
        if not owner:
            return None
        result["metadata"]["ownerReferences"] = [dict(owner, kind="Deployment", controller=True)]
    else:
        result["spec"] = {"replicas": value.get("spec", {}).get("replicas"),
                          "strategy": {"type": value.get("spec", {}).get("strategy", {}).get("type")}}
    return result


def observe_pod(run_id, pod, replica_set, deployment):
    identity = helpers.metadata(pod, run_id, role="application")
    rs = helpers.metadata(replica_set, run_id)
    dep = helpers.metadata(deployment, run_id)
    if (not identity or not rs or not dep or helpers.controller(pod, "ReplicaSet", rs) is None
            or helpers.controller(replica_set, "Deployment", dep) is None):
        return None
    containers = pod.get("spec", {}).get("containers", [])
    if len(containers) != 1 or containers[0].get("name") != "application":
        return None
    container = containers[0]
    image = container.get("image", "")
    if not re.fullmatch(r"cloudforge/healthy-worker:" + re.escape(run_id) + r"-[ab]", image):
        return None
    statuses = [item for item in pod.get("status", {}).get("containerStatuses", []) if item.get("name") == "application"]
    if len(statuses) > 1:
        raise helpers.QualificationError("worker_container_status_ambiguous")
    status = statuses[0] if statuses else {}
    restarts = status.get("restartCount", 0)
    if not isinstance(restarts, int) or not 0 <= restarts <= 10000:
        raise helpers.QualificationError("worker_restart_observation_invalid")
    process = {"running_observed": False, "maximum_container_restarts": restarts, "started_at": [], "terminations": []}
    running = status.get("state", {}).get("running", {}).get("startedAt", "")
    if running:
        if not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z", running):
            raise helpers.QualificationError("worker_start_observation_invalid")
        process["running_observed"] = True
        process["started_at"].append(running)
    for state in (status.get("state", {}), status.get("lastState", {})):
        terminated = state.get("terminated")
        if terminated:
            code = terminated.get("exitCode")
            if not isinstance(code, int) or not 0 <= code <= 255:
                raise helpers.QualificationError("worker_exit_observation_invalid")
            reason = terminated.get("reason")
            process["terminations"].append({"exit_code": code, "reason": reason if reason in ("Completed", "Error", "OOMKilled") else "other"})
    started = helpers.running_container(statuses, "application") or {}
    return {"run_id": run_id, "pod": identity, "replica_set": rs, "deployment": dep, **started, "process": process,
            "image": image, "no_http_probes": not any(container.get(name) for name in ("readinessProbe", "livenessProbe", "startupProbe")),
            "no_container_ports": not container.get("ports"),
            "replicas": deployment.get("spec", {}).get("replicas"),
            "strategy": deployment.get("spec", {}).get("strategy", {}).get("type")}


def merge_pod_observation(previous, current):
    """Retain bounded process facts from existing responses, never Pod messages."""
    for field in ("pod", "replica_set", "deployment", "image", "run_id"):
        if previous[field] != current[field]:
            raise helpers.QualificationError("worker_ownership_or_image_changed")
    before, after = previous["process"], current["process"]
    before["running_observed"] |= after["running_observed"]
    before["maximum_container_restarts"] = max(before["maximum_container_restarts"], after["maximum_container_restarts"])
    for field in ("started_at", "terminations"):
        for value in after[field]:
            if value not in before[field]:
                if len(before[field]) >= 8:
                    raise helpers.QualificationError("too_many_worker_process_observations")
                before[field].append(value)
    for field in ("no_http_probes", "no_container_ports"):
        previous[field] &= current[field]
    return previous


def maybe_mark_cancellation(state, case, marker):
    if marker.exists() or not state.get("candidate"):
        return
    candidate = state["candidate"]
    if time.monotonic() - candidate.get("observed_monotonic", 0) > 5:
        return
    count = len(state["pods"])
    is_replacement = count >= 2 and candidate["pod"]["uid"] != state["pods"][0]["pod"]["uid"]
    if (case == "cancel-startup" and count == 1) or (case == "cancel-recovery" and is_replacement):
        save(marker, dict(candidate, cloudforge_consumed_running_observation=True,
                          proof_scope="cancellation_after_owned_running_worker_observed"))


def kubectl_wrapper(arguments):
    helpers.require_runner()
    real = os.environ[ENV_PREFIX + "REAL_KUBECTL"]
    selected = "get" in arguments and any(kind in arguments for kind in ("pods", "deployment", "replicasets")) and "--context" in arguments
    if not selected:
        os.execv(real, [real, *arguments])
    context, run_id = helpers.context_identity(arguments)
    state_path = Path(os.environ[ENV_PREFIX + "STATE"])
    try:
        state = read_state(state_path)
    except (OSError, ValueError):
        state = {"pods": [], "observer_errors": ["state_unavailable"]}
    # A following pod observation proves CloudForge consumed the previous one.
    # Only broken fixture modes are used for cancellation, so that phase remains
    # waiting rather than racing through a successful heartbeat requirement.
    if "pods" in arguments:
        maybe_mark_cancellation(state, os.environ[ENV_PREFIX + "CASE"], Path(os.environ[ENV_PREFIX + "MARKER"]))
    result = helpers.capture([real, *arguments], 31)
    if result.returncode == 0:
        try:
            value = json.loads(result.stdout)
            if "deployment" in arguments:
                deployment = safe_controller_object(value, run_id, "Deployment")
                if deployment:
                    state["deployment"] = deployment
            if "replicasets" in arguments:
                replica_sets = [safe_controller_object(item, run_id, "ReplicaSet") for item in value.get("items", [])]
                state["replica_sets"] = [item for item in replica_sets if item][:8]
            items = value.get("items", []) if "pods" in arguments else []
            owned = [pod for pod in items if helpers.metadata(pod, run_id, role="application")]
            if len(owned) == 1:
                pod = owned[0]
                identity = helpers.metadata(pod, run_id, role="application")
                known = next((item for item in state["pods"] if item["pod"] == identity), None)
                owner = helpers.controller(pod, "ReplicaSet")
                rs = next((item for item in state.get("replica_sets", []) if helpers.metadata(item, run_id) == owner), None)
                deployment = state.get("deployment")
                observed = observe_pod(run_id, pod, rs, deployment) if deployment and rs else None
                if observed:
                    if known:
                        merge_pod_observation(known, observed)
                    else:
                        if len(state["pods"]) >= 8:
                            raise helpers.QualificationError("too_many_worker_observations")
                        state["pods"].append(observed)
                    if observed.get("started_at"):
                        state["candidate"] = dict(observed, observed_monotonic=time.monotonic())
                    # This private reference is never uploaded. The parent may
                    # make an independent Service read outside command deadlines.
                    save(Path(os.environ[ENV_PREFIX + "CONTEXT"]),
                         {"context": context, "run_id": run_id, "kubeconfig": os.environ["KUBECONFIG"]})
        except (OSError, ValueError, KeyError, TypeError, AttributeError, helpers.QualificationError, subprocess.SubprocessError) as error:
            if len(state["observer_errors"]) < 8:
                state["observer_errors"].append(type(error).__name__)
        try:
            save(state_path, state)
        except OSError:
            pass  # Missing qualification data is detected outside CloudForge.
    # Auxiliary observations never replace the real kubectl response/exit code.
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)
    if result.returncode < 0:
        received = -result.returncode
        if received not in (signal.SIGKILL, signal.SIGSTOP):
            signal.signal(received, signal.SIG_DFL)
        os.kill(os.getpid(), received)
    return result.returncode


def case_config(case):
    config = json.loads((REPOSITORY / "testdata/healthy-worker/cloudforge.yaml").read_text())
    mode = {"cancel-startup": "never", "cancel-recovery": "first-pod-only"}.get(case, case)
    if mode != "healthy":
        config["worker"]["command"].append(mode)
    return config


def inspect(binary, source, config, output):
    for name, arguments in (("analysis", ["analyze"]), ("plan", ["verify", "--plan"])):
        command = [binary, *arguments, str(source), "--config", str(config), "--format", "json"]
        first, second = helpers.capture(command, 30), helpers.capture(command, 30)
        (output / (name + ".stdout.json")).write_bytes(first.stdout)
        (output / (name + ".stderr.txt")).write_bytes(first.stderr)
        helpers.write_json(output / (name + ".command.json"), {"argv": command, "exit_code": first.returncode})
        if first.returncode or second.returncode or first.stdout != second.stdout:
            raise helpers.QualificationError("worker_inspection_failed_or_nondeterministic")
        if name == "plan":
            plan = json.loads(first.stdout)
            if plan["schema_version"] != "v1alpha7" or plan["runtime_kind"] != "worker" or plan["status"] != "pass":
                raise helpers.QualificationError("worker_plan_contract_mismatch")
            capabilities = {item["name"]: item["disposition"] for item in plan["capabilities"]}
            if any(capabilities.get(name) != "supported" for name in WORKER_IDS):
                raise helpers.QualificationError("worker_capability_not_planned")
            if any(capabilities.get(name) != "skipped" for name in HTTP_IDS):
                raise helpers.QualificationError("http_capability_in_worker_plan")


def require(condition, reason):
    if not condition:
        raise helpers.QualificationError(reason)


def timestamp(value):
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        require(parsed.tzinfo is not None, "worker_observation_timestamp_missing_zone")
        return parsed
    except (AttributeError, TypeError, ValueError):
        raise helpers.QualificationError("worker_observation_timestamp_invalid") from None


def check_sample(sample, fresh):
    require(0 < sample.get("ttl_ms", 0) <= 3000, "fixture_heartbeat_expiry_unproven")
    age = max(0, (timestamp(sample.get("redis_time")) - timestamp(sample.get("timestamp"))).total_seconds() * 1000)
    require(abs(age - sample.get("age_ms", -100)) <= 1, "worker_heartbeat_age_inconsistent")
    if fresh:
        require(0 <= sample["age_ms"] <= 30000, "worker_heartbeat_not_fresh")


def check_worker_process(worker, previous_uid=None):
    require(worker.get("pod_uid") and worker.get("key_absent_before_start") and worker.get("maximum_running_pods") == 1
            and worker.get("running_pods") == 1 and worker.get("container_restarts") == 0,
            "worker_single_uninterrupted_writer_unproven")
    timestamp(worker.get("process_started_at"))
    timestamp(worker.get("redis_observed_at"))
    if previous_uid is not None:
        require(previous_uid and worker.get("previous_pod_uid") == previous_uid and worker["pod_uid"] != previous_uid
                and worker.get("predecessor_terminated") and worker.get("previous_heartbeat_gone"),
                "worker_predecessor_boundary_unproven")


def check_progress(worker, previous_uid=None):
    check_worker_process(worker, previous_uid)
    samples = worker.get("samples", [])
    require(worker.get("heartbeat_state") == "advancing" and len(samples) == 2, "worker_fresh_bounded_progress_unproven")
    for sample in samples:
        check_sample(sample, fresh=True)
        require(timestamp(sample["timestamp"]) >= timestamp(worker["process_started_at"]), "worker_heartbeat_predates_process")
    require(timestamp(samples[1]["timestamp"]) > timestamp(samples[0]["timestamp"]), "worker_progress_timestamps_not_advancing")
    require(timestamp(samples[1]["redis_time"]) >= timestamp(samples[0]["redis_time"])
            and timestamp(samples[-1]["redis_time"]) == timestamp(worker["redis_observed_at"]), "worker_redis_observation_inconsistent")


def check_missing(worker, previous_uid=None):
    check_worker_process(worker, previous_uid)
    require(worker.get("heartbeat_state") == "missing" and worker.get("samples") == [], "worker_missing_heartbeat_not_observed")


def check_deadline(item):
    measurements = {value["name"]: value["value"] for value in item.get("measurements", [])}
    require(measurements.get("requirement_deadline_exceeded") == "true"
            and measurements.get("final_state_observed_after_deadline") == "true", "worker_failure_deadline_not_observed")
    require(item.get("summary") == "The configured fresh, expiring and advancing heartbeat remained unsatisfied after the requirement deadline.",
            "worker_failure_not_heartbeat_requirement")


def check_report(report, code, case):
    evidence = {item["experiment_id"]: item for item in report["evidence"]}

    require(report["schema_version"] == "v1alpha7" and report["plan"]["runtime_kind"] == "worker", "worker_report_contract_mismatch")
    require(report.get("producer", {}).get("commit") not in (None, "", "unknown"), "worker_producer_unidentified")
    require("cloudforge:worker:heartbeat" not in json.dumps(report) and "worker.js" not in json.dumps(report), "worker_contract_values_exposed")
    for name in HTTP_IDS:
        require(evidence.get(name, {}).get("status") == "skipped" and not evidence[name].get("execution", {}).get("executed"), "worker_report_claims_http_execution")
    for name in ("container-build", "dependency.redis", "network-isolation"):
        require(evidence.get(name, {}).get("status") == "pass", "worker_prerequisite_not_established")
    for name in WORKER_IDS:
        item = evidence.get(name, {})
        if item.get("status") != "pass":
            continue
        worker = item.get("worker", {})
        if name != "worker-startup":
            require(worker.get("previous_pod_uid"), "worker_predecessor_identity_missing")
        check_progress(worker, worker.get("previous_pod_uid") if name != "worker-startup" else None)
        if name != "worker-startup":
            require(item.get("recovery", {}).get("status") == "pass", "healthy_worker_baseline_not_restored")
    for name in WORKER_IDS[1:]:
        item = evidence.get(name, {})
        recovery = item.get("recovery", {})
        if recovery.get("status") == "pass":
            require(recovery.get("strategy") == "stop_wait_for_expiry_restore_worker_and_validate", "worker_restoration_strategy_unproven")
            check_progress(recovery.get("worker", {}), item.get("worker", {}).get("pod_uid"))
    if case.startswith("cancel-"):
        target = "worker-startup" if case == "cancel-startup" else "worker-recovery"
        require(code == 2 and report["status"] == "error", "worker_cancellation_not_error")
        require(any(d.get("code") == "verification_canceled" for d in report.get("diagnostics", [])), "worker_cancellation_unclassified")
        require(evidence[target]["status"] == "error" and evidence[target].get("execution", {}).get("executed"), "started_worker_experiment_not_preserved")
        require(not evidence["worker-image-replacement"].get("execution", {}).get("executed"), "worker_scheduled_after_cancellation")
        return
    if case == "healthy":
        require(code == 0 and report["status"] in ("pass", "warn"), "healthy_worker_not_qualified")
        for name in WORKER_IDS:
            require(evidence[name]["status"] == "pass" and evidence[name].get("execution", {}).get("executed"), "healthy_worker_evidence_missing")
    elif case in ("future", "malformed"):
        require(code == 2 and report["status"] == "error" and evidence["worker-startup"]["status"] == "error", "invalid_worker_observation_became_requirement_failure")
        item = evidence["worker-startup"]
        require(item.get("summary") == "Worker heartbeat was malformed, clock-uncertain or could not be observed reliably."
                and item.get("worker", {}).get("samples") == [], "invalid_worker_observation_cause_unproven")
    elif case in ("never", "stale", "frozen", "exit"):
        require(code == 1 and report["status"] == "fail" and evidence["worker-startup"]["status"] == "fail", "valid_worker_requirement_failure_not_retained")
        require(not evidence["worker-recovery"].get("execution", {}).get("executed"), "worker_recovery_executed_without_startup")
        item = evidence["worker-startup"]
        worker = item.get("worker", {})
        if case == "exit":
            require(worker.get("container_restarts", 0) > 0 and worker.get("samples") == []
                    and worker.get("key_absent_before_start") and worker.get("maximum_running_pods", 2) <= 1,
                    "worker_exit_restart_evidence_missing")
            require(item.get("summary") == "The worker container restarted instead of establishing uninterrupted heartbeat progress.",
                    "worker_failure_not_process_restart")
        else:
            check_deadline(item)
            check_worker_process(worker)
            samples = worker.get("samples", [])
            if case == "never":
                check_missing(worker)
            else:
                require(worker.get("heartbeat_state") in ("stale", "not_advancing"), "worker_stale_or_frozen_state_unproven")
                require(len(samples) == (2 if case == "stale" else 1), "worker_stale_or_frozen_sample_count_wrong")
                for sample in samples:
                    check_sample(sample, fresh=False)
                    require(sample["age_ms"] > 30000, "worker_stale_or_frozen_age_unproven")
                require(timestamp(samples[-1]["redis_time"]) == timestamp(worker["redis_observed_at"]), "worker_final_heartbeat_observation_inconsistent")
                if case == "stale":
                    require(worker["heartbeat_state"] == "stale" and timestamp(samples[1]["timestamp"]) > timestamp(samples[0]["timestamp"]),
                            "worker_stale_timestamp_updates_unproven")
    else:
        require(code == 1 and report["status"] == "fail", "worker_recovery_failure_disappeared")
        require(evidence["worker-startup"]["status"] == "pass" and evidence["worker-recovery"]["status"] == "fail", "old_worker_heartbeat_satisfied_new_process")
        recovery = evidence["worker-recovery"]
        check_deadline(recovery)
        check_missing(recovery.get("worker", {}), evidence["worker-startup"]["worker"]["pod_uid"])
        if case == "fail-second-start":
            require(evidence["worker-recovery"].get("recovery", {}).get("status") == "pass", "worker_baseline_not_restored")
            require(evidence["worker-image-replacement"]["status"] == "pass" and evidence["worker-image-replacement"].get("execution", {}).get("executed"), "worker_did_not_continue_after_restoration")
        else:
            require(recovery.get("recovery", {}).get("status") == "blocked", "worker_false_restoration")
            check_missing(recovery["recovery"].get("worker", {}), recovery["worker"]["pod_uid"])
            require(evidence["worker-image-replacement"]["status"] == "blocked" and not evidence["worker-image-replacement"].get("execution", {}).get("executed"), "worker_continued_without_restoration")


def check_observations(report, observations, case):
    """Match native evidence to cumulative controller-linked Pod observations."""
    require(not observations.get("observer_errors"), "worker_ownership_observation_failed")
    pods = observations.get("pods", [])
    require(pods and len({pod["pod"]["uid"] for pod in pods}) == len(pods), "worker_distinct_owned_pods_unobserved")
    by_uid = {pod["pod"]["uid"]: pod for pod in pods}
    run_id = report.get("run_id")
    require(run_id and all(pod["run_id"] == run_id for pod in pods), "worker_run_identity_mismatch")
    for pod in pods:
        require(pod["no_http_probes"] and pod["no_container_ports"] and pod["replicas"] == 1 and pod["strategy"] == "Recreate",
                "worker_runtime_topology_mismatch")
        process = pod.get("process", {})
        if case != "exit":
            require(process.get("maximum_container_restarts") == 0 and process.get("terminations") == [], "worker_unexpected_process_failure")

    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    chain = []
    for name in WORKER_IDS:
        item = evidence.get(name, {})
        worker = item.get("worker", {})
        if worker.get("pod_uid"):
            chain.append((worker, "b" if name == "worker-image-replacement" else "a"))
        restored = item.get("recovery", {}).get("worker", {})
        if restored.get("pod_uid"):
            chain.append((restored, "a"))
    expected_count = 5 if case in ("healthy", "fail-second-start") else 3 if case == "first-pod-only" else 1
    if not case.startswith("cancel-"):
        require(len(chain) == expected_count and [worker["pod_uid"] for worker, _ in chain] == [pod["pod"]["uid"] for pod in pods],
                "worker_native_and_observed_transition_chain_mismatch")
    previous = None
    for worker, version in chain:
        uid = worker["pod_uid"]
        require(uid in by_uid, "native_worker_pod_not_independently_observed")
        pod = by_uid[uid]
        process = pod["process"]
        image = "cloudforge/healthy-worker:" + run_id + "-" + version
        require(worker.get("image") == image and pod["image"] == image, "worker_native_and_observed_image_mismatch")
        if previous:
            require(worker.get("previous_pod_uid") == previous and uid != previous, "worker_native_predecessor_chain_mismatch")
        else:
            require(not worker.get("previous_pod_uid"), "worker_startup_has_unexpected_predecessor")
        previous = uid
        if case == "exit":
            exits = process.get("terminations", [])
            require(exits and all(value == {"exit_code": 23, "reason": "Error"} for value in exits), "worker_intended_exit_23_unobserved")
            require(process.get("maximum_container_restarts") == worker.get("container_restarts", 0) > 0, "worker_native_restart_not_independently_observed")
        elif worker.get("process_started_at") or (not case.startswith("cancel-") and case not in ("future", "malformed")):
            # An invalid Redis value can arrive between a Pending Pod snapshot
            # and the heartbeat read. Preserve that observation ERROR without
            # inventing a process-start observation that Kubernetes never gave.
            require(process.get("running_observed") and any(timestamp(started) == timestamp(worker.get("process_started_at"))
                    for started in process.get("started_at", [])), "worker_native_process_instance_unobserved")


def run(binary, source, config, output, case, qualification):
    real = shutil.which("kubectl")
    if not real:
        raise helpers.QualificationError("kubectl_missing")
    image = re.search(r"(?m)^FROM (\S+@sha256:[a-f0-9]{64})$", (source / "Dockerfile").read_text()).group(1)
    with tempfile.TemporaryDirectory(prefix="cf-worker-qualification-") as temporary, helpers.existing_state(image) as state:
        private = Path(temporary)
        wrapper = private / "kubectl"
        wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable,os.environ['CF_WORKER_QUALIFY_HARNESS'],'__kubectl',*sys.argv[1:]])\n")
        wrapper.chmod(0o700)
        state_path, marker = private / "observations.json", private / "cancel-marker.json"
        context_path = private / "observer-context.json"
        environment = dict(os.environ, TMPDIR=str(private), PATH=str(private) + os.pathsep + os.environ["PATH"])
        environment.update({ENV_PREFIX + "HARNESS": str(Path(__file__).resolve()), ENV_PREFIX + "REAL_KUBECTL": real,
                            ENV_PREFIX + "STATE": str(state_path), ENV_PREFIX + "MARKER": str(marker),
                            ENV_PREFIX + "CONTEXT": str(context_path), ENV_PREFIX + "CASE": case})
        command = [binary, "verify", str(source), "--config", str(config), "--format", "json"]
        helpers.write_json(output / "command.json", {"argv": command})
        process, interrupted = None, None
        service_proof = None
        try:
            with (output / "stdout.json").open("wb") as stdout, (output / "stderr.txt").open("wb") as stderr:
                process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=environment)
                qualification["runtime_executed"] = True
                deadline = time.monotonic() + VERIFY_SECONDS
                while process.poll() is None and time.monotonic() < deadline:
                    if case.startswith("cancel-") and marker.exists():
                        observed = json.loads(marker.read_text())
                        if time.monotonic() - observed["observed_monotonic"] > 5:
                            raise helpers.QualificationError("worker_cancellation_observation_stale")
                        qualification["interruption"] = observed
                        interrupted = time.monotonic()
                        process.send_signal(signal.SIGINT)
                        process.wait(timeout=CLEANUP_SECONDS)
                        break
                    if not case.startswith("cancel-") and service_proof is None and context_path.exists():
                        context = json.loads(context_path.read_text())
                        isolated = dict(environment, KUBECONFIG=context["kubeconfig"])
                        try:
                            services = helpers.capture([real, "--context", context["context"], "--namespace", "cloudforge", "--request-timeout=2s",
                                                        "get", "services", "--selector=app.kubernetes.io/managed-by=cloudforge,cloudforge.dev/run-id="
                                                        + context["run_id"] + ",cloudforge.dev/role=application", "--output=json"], 3, environment=isolated)
                            service_proof = {"scope": "separate_read_outside_cloudforge_command_deadline",
                                             "observed": services.returncode == 0,
                                             "no_application_service": services.returncode == 0 and json.loads(services.stdout).get("items") == []}
                        except (OSError, ValueError, helpers.QualificationError, subprocess.SubprocessError) as error:
                            service_proof = {"observed": False, "error_type": type(error).__name__}
                        helpers.write_json(output / "application-service-observation.json", service_proof)
                    time.sleep(0.1)
                if process.poll() is None:
                    raise helpers.QualificationError("worker_verification_timeout")
            if case.startswith("cancel-") and interrupted is None:
                raise helpers.QualificationError("owned_worker_cancellation_phase_not_observed")
            report = json.loads((output / "stdout.json").read_text())
            check_report(report, process.returncode, case)
            observations = read_state(state_path)
            helpers.write_json(output / "worker-observations.json", observations)
            check_observations(report, observations, case)
            if case in ("healthy", "never", "stale", "frozen", "first-pod-only", "fail-second-start"):
                if not service_proof or not service_proof.get("observed") or not service_proof.get("no_application_service"):
                    raise helpers.QualificationError("worker_application_service_absence_unobserved")
            for format_name, file_name in (("json", "canonical.json"), ("text", "report.txt"), ("markdown", "report.md")):
                arguments = [binary, "report", str(output / "stdout.json"), "--format", format_name]
                result = helpers.capture(arguments, 30, 4 * 1024 * 1024)
                (output / file_name).write_bytes(result.stdout)
                (output / (file_name + ".stderr.txt")).write_bytes(result.stderr)
                if result.returncode:
                    raise helpers.QualificationError("worker_report_reload_failed")
                if format_name == "json" and result.stdout != helpers.capture(arguments, 30, 4 * 1024 * 1024).stdout:
                    raise helpers.QualificationError("worker_report_serialization_unstable")
            qualification["native_status"] = report["status"]
        finally:
            if process is not None and process.poll() is None:
                if interrupted is None:
                    interrupted = time.monotonic()
                    process.send_signal(signal.SIGINT)
                    qualification["fallback_cleanup_signal"] = "SIGINT"
                try:
                    process.wait(timeout=max(0, CLEANUP_SECONDS - (time.monotonic() - interrupted)))
                except subprocess.TimeoutExpired:
                    qualification["forced_kill"] = True
                    process.kill()
                    process.wait(timeout=10)
            if process is not None:
                helpers.write_json(output / "exit.json", {"exit_code": process.returncode})
            if state_path.exists():
                helpers.write_json(output / "worker-observations.json", read_state(state_path))
            qualification["cleanup"] = helpers.preserve_cleanup(REPOSITORY, output, private, state)
            if not qualification["cleanup"]["passed"]:
                raise helpers.QualificationError("worker_cleanup_or_unrelated_state_failed")


def self_test():
    class Tests(unittest.TestCase):
        def qualification_case(self, case="healthy"):
            run_id = "test-run"
            def worker(index, previous=None, version="a"):
                second = index * 10
                started = f"2026-09-21T10:00:{second:02d}Z"
                samples = [{"timestamp": f"2026-09-21T10:00:{second + beat:02d}Z",
                            "redis_time": f"2026-09-21T10:00:{second + beat:02d}.100Z", "age_ms": 100, "ttl_ms": 2500} for beat in (1, 2)]
                return {"pod_uid": "pod-" + str(index), "previous_pod_uid": previous, "image": "cloudforge/healthy-worker:" + run_id + "-" + version,
                        "running_pods": 1, "maximum_running_pods": 1, "container_restarts": 0, "key_absent_before_start": True,
                        "predecessor_terminated": bool(previous), "previous_heartbeat_gone": bool(previous), "heartbeat_state": "advancing",
                        "process_started_at": started, "redis_observed_at": samples[-1]["redis_time"], "samples": samples}
            workers = [worker(1), worker(2, "pod-1"), worker(3, "pod-2"), worker(4, "pod-3", "b"), worker(5, "pod-4")]
            items = [{"experiment_id": name, "status": "skipped", "execution": {"executed": False}} for name in HTTP_IDS]
            items += [{"experiment_id": name, "status": "pass"} for name in ("container-build", "dependency.redis", "network-isolation")]
            for name, index in zip(WORKER_IDS, (0, 1, 3)):
                item = {"experiment_id": name, "status": "pass", "worker": workers[index], "execution": {"executed": True}}
                if index:
                    item["recovery"] = {"status": "pass", "strategy": "stop_wait_for_expiry_restore_worker_and_validate", "worker": workers[index + 1]}
                items.append(item)
            report = {"schema_version": "v1alpha7", "run_id": run_id, "plan": {"runtime_kind": "worker"}, "producer": {"commit": "test-commit"},
                      "status": "pass", "evidence": items}
            observations = {"observer_errors": [], "pods": []}
            for value in workers:
                observations["pods"].append({"run_id": run_id, "pod": {"uid": value["pod_uid"]}, "image": value["image"],
                                             "no_http_probes": True, "no_container_ports": True, "replicas": 1, "strategy": "Recreate",
                                             "process": {"running_observed": True, "maximum_container_restarts": 0,
                                                         "started_at": [value["process_started_at"]], "terminations": []}})
            evidence = {item["experiment_id"]: item for item in items}
            def missing(item):
                item.update(status="fail", summary="The configured fresh, expiring and advancing heartbeat remained unsatisfied after the requirement deadline.",
                            measurements=[{"name": "requirement_deadline_exceeded", "value": "true"}, {"name": "final_state_observed_after_deadline", "value": "true"}])
                item["worker"].update(heartbeat_state="missing", samples=[])
            if case in ("first-pod-only", "fail-second-start"):
                report["status"] = "fail"
                missing(evidence["worker-recovery"])
                if case == "first-pod-only":
                    restored = evidence["worker-recovery"]["recovery"]
                    restored["status"] = "blocked"
                    restored["worker"].update(heartbeat_state="missing", samples=[])
                    evidence["worker-image-replacement"].update(status="blocked", execution={"executed": False})
                    evidence["worker-image-replacement"].pop("worker")
                    evidence["worker-image-replacement"].pop("recovery")
                    observations["pods"] = observations["pods"][:3]
            elif case != "healthy":
                report["status"] = "fail"
                startup = evidence["worker-startup"]
                missing(startup)
                for name in WORKER_IDS[1:]:
                    evidence[name].update(status="blocked", execution={"executed": False})
                    evidence[name].pop("worker")
                    evidence[name].pop("recovery")
                observations["pods"] = observations["pods"][:1]
                if case in ("stale", "frozen"):
                    count = 2 if case == "stale" else 1
                    startup["worker"].update(heartbeat_state="stale", samples=[{
                        "timestamp": f"2026-09-21T10:00:{beat:02d}Z", "redis_time": f"2026-09-21T10:02:{beat:02d}.100Z",
                        "age_ms": 120100, "ttl_ms": 2500} for beat in range(count)])
                    startup["worker"]["redis_observed_at"] = startup["worker"]["samples"][-1]["redis_time"]
                elif case == "exit":
                    startup["summary"] = "The worker container restarted instead of establishing uninterrupted heartbeat progress."
                    startup["worker"].update(container_restarts=1, running_pods=0, maximum_running_pods=0)
                    observations["pods"][0]["process"].update(running_observed=False, maximum_container_restarts=1, started_at=[],
                                                            terminations=[{"exit_code": 23, "reason": "Error"}])
                elif case in ("future", "malformed"):
                    report["status"] = startup["status"] = "error"
                    startup["summary"] = "Worker heartbeat was malformed, clock-uncertain or could not be observed reliably."
            return report, observations, 0 if case == "healthy" else 2 if case in ("future", "malformed") else 1

        def test_intended_evidence_cases_qualify(self):
            for case in CASES:
                if case.startswith("cancel-"):
                    continue
                with self.subTest(case=case):
                    report, observations, code = self.qualification_case(case)
                    check_report(report, code, case)
                    check_observations(report, observations, case)

        def test_invalid_heartbeat_can_precede_running_pod_snapshot(self):
            for case in ("future", "malformed"):
                with self.subTest(case=case):
                    report, observations, code = self.qualification_case(case)
                    startup = next(item for item in report["evidence"] if item["experiment_id"] == "worker-startup")
                    startup["worker"].pop("process_started_at")
                    startup["worker"].pop("redis_observed_at")
                    startup["worker"].update(running_pods=0, maximum_running_pods=0)
                    observations["pods"][0]["process"].update(running_observed=False, started_at=[])
                    check_report(report, code, case)
                    check_observations(report, observations, case)
                    for fault in ("image", "uid", "status"):
                        changed = copy.deepcopy(report)
                        item = next(value for value in changed["evidence"] if value["experiment_id"] == "worker-startup")
                        if fault == "status":
                            item["status"] = "fail"
                        else:
                            item["worker"]["image" if fault == "image" else "pod_uid"] = "unrelated"
                        with self.assertRaises(helpers.QualificationError):
                            check_report(changed, code, case)
                            check_observations(changed, observations, case)
            for case in ("healthy", "never", "stale", "frozen"):
                with self.subTest(required_start_case=case):
                    report, observations, code = self.qualification_case(case)
                    next(item for item in report["evidence"] if item["experiment_id"] == "worker-startup")["worker"].pop("process_started_at")
                    with self.assertRaises(helpers.QualificationError):
                        check_report(report, code, case)
                        check_observations(report, observations, case)

        def test_heartbeat_faults_cannot_qualify_crashes_or_wrong_heartbeat(self):
            for case in ("never", "stale", "frozen"):
                for fault in ("restart", "wrong_state", "deadline", "summary", "oom"):
                    with self.subTest(case=case, fault=fault):
                        report, observations, code = self.qualification_case(case)
                        startup = next(item for item in report["evidence"] if item["experiment_id"] == "worker-startup")
                        if fault == "restart":
                            startup["worker"]["container_restarts"] = 1
                        elif fault == "wrong_state":
                            startup["worker"]["heartbeat_state"] = "process_baseline_unready"
                        elif fault == "deadline":
                            startup["measurements"][1]["value"] = "false"
                        elif fault == "summary":
                            startup["summary"] = "The worker container restarted instead of establishing uninterrupted heartbeat progress."
                        else:
                            observations["pods"][0]["process"]["terminations"] = [{"exit_code": 137, "reason": "OOMKilled"}]
                        with self.assertRaises(helpers.QualificationError):
                            check_report(report, code, case)
                            check_observations(report, observations, case)
            for case, count in (("stale", 1), ("frozen", 2)):
                report, _, code = self.qualification_case(case)
                worker = next(item for item in report["evidence"] if item["experiment_id"] == "worker-startup")["worker"]
                worker["samples"] = [worker["samples"][0]] * count
                with self.assertRaises(helpers.QualificationError):
                    check_report(report, code, case)
            for ttl in (0, 3001):
                report, _, code = self.qualification_case("stale")
                next(item for item in report["evidence"] if item["experiment_id"] == "worker-startup")["worker"]["samples"][0]["ttl_ms"] = ttl
                with self.assertRaises(helpers.QualificationError):
                    check_report(report, code, "stale")

        def test_exit_case_requires_observed_exit_23_not_oom_or_generic_failure(self):
            for terminal in ({"exit_code": 137, "reason": "OOMKilled"}, {"exit_code": 24, "reason": "Error"}, {"exit_code": 23, "reason": "other"}, None):
                with self.subTest(terminal=terminal):
                    report, observations, _ = self.qualification_case("exit")
                    observations["pods"][0]["process"]["terminations"] = [terminal] if terminal else []
                    with self.assertRaises(helpers.QualificationError):
                        check_observations(report, observations, "exit")

        def test_restoration_pass_requires_full_fresh_progress(self):
            for case in ("healthy", "fail-second-start"):
                for name in WORKER_IDS[1:]:
                    for fault in ("samples", "expiry", "restart", "previous", "absence", "state", "age", "start"):
                        with self.subTest(case=case, name=name, fault=fault):
                            report, observations, code = self.qualification_case(case)
                            worker = next(item for item in report["evidence"] if item["experiment_id"] == name)["recovery"]["worker"]
                            if fault == "samples":
                                worker["samples"] = []
                            elif fault == "expiry":
                                worker["samples"][0]["ttl_ms"] = 0
                            elif fault == "restart":
                                worker["container_restarts"] = 1
                            elif fault == "previous":
                                worker["previous_pod_uid"] = "unrelated"
                            elif fault == "absence":
                                worker["previous_heartbeat_gone"] = False
                            elif fault == "state":
                                worker["heartbeat_state"] = "stale"
                            elif fault == "age":
                                worker["samples"][0]["age_ms"] = 0
                            else:
                                worker["process_started_at"] = "2026-09-21T10:03:00Z"
                            with self.assertRaises(helpers.QualificationError):
                                check_report(report, code, case)
                                check_observations(report, observations, case)

        def test_five_pods_do_not_prove_native_identity_image_or_final_restoration(self):
            for fault in ("last_image", "last_uid", "last_process", "reordered", "predecessor", "wrong_run", "missing_native_recovery"):
                with self.subTest(fault=fault):
                    report, observations, code = self.qualification_case()
                    items = {item["experiment_id"]: item for item in report["evidence"]}
                    if fault == "last_image":
                        observations["pods"][-1]["image"] = observations["pods"][-2]["image"]
                    elif fault == "last_uid":
                        observations["pods"][-1]["pod"]["uid"] = "another-fifth-pod"
                    elif fault == "last_process":
                        observations["pods"][-1]["process"]["started_at"] = ["2026-09-21T10:04:00Z"]
                    elif fault == "reordered":
                        observations["pods"][-1], observations["pods"][-2] = observations["pods"][-2], observations["pods"][-1]
                    elif fault == "predecessor":
                        items["worker-image-replacement"]["worker"]["previous_pod_uid"] = "pod-1"
                    elif fault == "wrong_run":
                        observations["pods"][-1]["run_id"] = "other-run"
                    else:
                        items["worker-image-replacement"]["recovery"].pop("worker")
                    with self.assertRaises(helpers.QualificationError):
                        check_report(report, code, "healthy")
                        check_observations(report, observations, "healthy")

        def test_fixture_control_key_outlives_all_qualification_bounds(self):
            fixture = (REPOSITORY / "testdata/healthy-worker/worker.js").read_text()
            marker = re.search(r"const CONTROL_TTL_SECONDS = (\d+);", fixture)
            self.assertIsNotNone(marker)
            self.assertGreater(int(marker.group(1)), VERIFY_SECONDS + CLEANUP_SECONDS)
            self.assertEqual(int(marker.group(1)), 3600)
            self.assertIn('["EXPIRE", OWNER_KEY, CONTROL_TTL_SECONDS]', fixture)
            self.assertIn('["SET", OWNER_KEY, os.hostname(), "NX", "EX", CONTROL_TTL_SECONDS]', fixture)
            self.assertIn('["SET", HEARTBEAT_KEY, payload, "EX", "3"]', fixture)

        def test_modes_are_explicit(self):
            for case in CASES:
                config = case_config(case)
                self.assertEqual(config["runtime"]["kind"], "worker")
                self.assertEqual(config["worker"]["command"][:2], ["node", "worker.js"])
                self.assertNotIn("port", config["runtime"])
                self.assertEqual(config["topology"]["replicas"], 1)
                self.assertEqual(config["topology"]["rollout"]["strategy"], "recreate")

        def test_cancellation_requires_new_running_identity(self):
            with tempfile.TemporaryDirectory() as temporary:
                marker = Path(temporary) / "marker.json"
                state = {"pods": [{"pod": {"uid": "old"}}], "candidate": {"pod": {"uid": "old"}, "observed_monotonic": time.monotonic()}}
                maybe_mark_cancellation(state, "cancel-recovery", marker)
                self.assertFalse(marker.exists())
                state["pods"].append({"pod": {"uid": "new"}})
                maybe_mark_cancellation(state, "cancel-recovery", marker)
                self.assertFalse(marker.exists(), "old pod must not establish replacement cancellation")
                state["candidate"] = {"pod": {"uid": "new"}, "observed_monotonic": time.monotonic()}
                maybe_mark_cancellation(state, "cancel-recovery", marker)
                self.assertEqual(json.loads(marker.read_text())["pod"]["uid"], "new")
                marker.unlink()
                state["candidate"]["observed_monotonic"] -= 10
                maybe_mark_cancellation(state, "cancel-recovery", marker)
                self.assertFalse(marker.exists())

        def test_guard_precedes_runtime_side_effects(self):
            with tempfile.TemporaryDirectory() as temporary:
                output = Path(temporary) / "not-created"
                environment = dict(os.environ, GITHUB_ACTIONS="false")
                result = subprocess.run([sys.executable, __file__, "/missing", str(output), "--case", "healthy", "--run"], env=environment, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 2)
                self.assertFalse(output.exists())

        def test_passive_observation_checks_ownership_and_omits_payload(self):
            run_id = "test-run"
            labels = {"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": run_id, "cloudforge.dev/role": "application"}
            deployment = {"metadata": {"name": "worker", "namespace": "cloudforge", "uid": "dep-uid", "labels": labels},
                          "spec": {"replicas": 1, "strategy": {"type": "Recreate"}, "template": {"secret": "SECRET-CANARY"}}}
            rs = {"metadata": {"name": "worker-rs", "namespace": "cloudforge", "uid": "rs-uid", "labels": labels,
                               "ownerReferences": [{"kind": "Deployment", "controller": True, "name": "worker", "uid": "dep-uid"}]}}
            pod = {"metadata": {"name": "worker-pod", "namespace": "cloudforge", "uid": "pod-uid", "labels": labels,
                                "ownerReferences": [{"kind": "ReplicaSet", "controller": True, "name": "worker-rs", "uid": "rs-uid"}]},
                   "spec": {"containers": [{"name": "application", "image": "cloudforge/healthy-worker:test-run-a", "env": [{"value": "SECRET-CANARY"}]}]},
                   "status": {"containerStatuses": [{"name": "application", "state": {"running": {"startedAt": "2026-09-21T10:00:00Z"}}}], "message": "SECRET-CANARY"}}
            safe_dep = safe_controller_object(deployment, run_id, "Deployment")
            safe_rs = safe_controller_object(rs, run_id, "ReplicaSet")
            value = observe_pod(run_id, pod, safe_rs, safe_dep)
            self.assertTrue(value["no_http_probes"] and value["no_container_ports"])
            self.assertNotIn("SECRET-CANARY", json.dumps([safe_dep, safe_rs, value]))
            crashed = copy.deepcopy(pod)
            crashed["status"]["containerStatuses"][0].update(restartCount=1, state={"waiting": {"reason": "SECRET-CANARY"}},
                                                          lastState={"terminated": {"exitCode": 23, "reason": "Error", "message": "SECRET-CANARY"}})
            updated = observe_pod(run_id, crashed, safe_rs, safe_dep)
            merged = merge_pod_observation(copy.deepcopy(value), updated)
            self.assertTrue(merged["process"]["running_observed"])
            self.assertEqual(merged["process"]["started_at"], ["2026-09-21T10:00:00Z"])
            self.assertEqual(merged["process"]["maximum_container_restarts"], 1)
            self.assertEqual(merged["process"]["terminations"], [{"exit_code": 23, "reason": "Error"}])
            self.assertNotIn("SECRET-CANARY", json.dumps(merged))
            crashed["status"]["containerStatuses"][0]["lastState"]["terminated"].update(exitCode=137, reason="OOMKilled")
            updated = observe_pod(run_id, crashed, safe_rs, safe_dep)
            self.assertEqual(updated["process"]["terminations"], [{"exit_code": 137, "reason": "OOMKilled"}])
            crashed["status"]["containerStatuses"][0]["lastState"]["terminated"]["reason"] = "SECRET-CANARY"
            self.assertEqual(observe_pod(run_id, crashed, safe_rs, safe_dep)["process"]["terminations"][0]["reason"], "other")
            pod["metadata"]["ownerReferences"][0]["uid"] = "unrelated"
            self.assertIsNone(observe_pod(run_id, pod, safe_rs, safe_dep))
            pod["metadata"]["ownerReferences"][0]["uid"] = "rs-uid"
            pod["spec"]["containers"][0]["readinessProbe"] = {"tcpSocket": {"port": 80}}
            self.assertFalse(observe_pod(run_id, pod, safe_rs, safe_dep)["no_http_probes"])

    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(Tests))
    return 0 if result.wasSuccessful() else 1


def main():
    if sys.argv[1:2] == ["__kubectl"]:
        try:
            return kubectl_wrapper(sys.argv[2:])
        except (OSError, ValueError, KeyError, TypeError, helpers.QualificationError, subprocess.SubprocessError):
            print("worker qualification observer failed within its bound", file=sys.stderr)
            return 2
    if sys.argv[1:] == ["--self-test"]:
        return self_test()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("output", type=Path)
    parser.add_argument("--case", choices=CASES, default="healthy")
    parser.add_argument("--run", action="store_true")
    args = parser.parse_args()
    if args.run:
        helpers.require_runner()
    source = REPOSITORY / "testdata/healthy-worker"
    before = helpers.hashes(source)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    config = output / "runtime.yaml"
    helpers.write_json(config, case_config(args.case))
    qualification = {"schema_version": "v1", "case": args.case, "runtime_executed": False, "qualified": False,
                     "business_job_completion_tested": False, "source_hashes": before,
                     "secret_objects_queried": False, "application_logs_queried": False,
                     "bounds_seconds": {"verification": VERIFY_SECONDS, "cleanup": CLEANUP_SECONDS}}
    error = None
    try:
        binary = str(Path(args.binary).resolve())
        inspect(binary, source, config, output)
        qualification["read_only_plan_passed"] = True
        if args.run:
            run(binary, source, config, output, args.case, qualification)
            qualification["qualified"] = True
    except (OSError, ValueError, KeyError, TypeError, helpers.QualificationError, subprocess.SubprocessError) as exception:
        error = str(exception) if isinstance(exception, helpers.QualificationError) else "worker_qualification_execution_error"
        qualification["failure"] = error
    finally:
        qualification["original_fixture_unchanged"] = before == helpers.hashes(source)
        if not qualification["original_fixture_unchanged"]:
            error = qualification["failure"] = "worker_fixture_changed"
        if error:
            qualification["qualified"] = False
        helpers.write_json(output / "qualification.json", qualification)
    if error:
        print(error, file=sys.stderr)
        return 1
    print("worker runtime qualified: " + args.case if args.run else "worker read-only plan checked; runtime not executed")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except helpers.QualificationError as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
