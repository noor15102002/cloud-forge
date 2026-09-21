#!/usr/bin/env python3
"""Qualify bounded Redis-heartbeat workers while retaining exact CLI evidence.

Default execution is read-only analyze/plan. --run requires disposable hosted
Linux. Runtime cases never claim queue job completion or business correctness.
"""
import argparse
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
    started = helpers.running_container(pod.get("status", {}).get("containerStatuses", []), "application")
    if not started:
        return None
    return {"run_id": run_id, "pod": identity, "replica_set": rs, "deployment": dep, **started,
            "image": image, "no_http_probes": not any(container.get(name) for name in ("readinessProbe", "livenessProbe", "startupProbe")),
            "no_container_ports": not container.get("ports"),
            "replicas": deployment.get("spec", {}).get("replicas"),
            "strategy": deployment.get("spec", {}).get("strategy", {}).get("type")}


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
                if known:
                    started = helpers.running_container(pod.get("status", {}).get("containerStatuses", []), "application")
                    if started:
                        state["candidate"] = dict(known, observed_monotonic=time.monotonic())
                else:
                    owner = helpers.controller(pod, "ReplicaSet")
                    rs = next((item for item in state.get("replica_sets", []) if helpers.metadata(item, run_id) == owner), None)
                    deployment = state.get("deployment")
                    observed = observe_pod(run_id, pod, rs, deployment) if deployment else None
                    if observed:
                        if len(state["pods"]) >= 8:
                            raise helpers.QualificationError("too_many_worker_observations")
                        state["pods"].append(observed)
                        state["candidate"] = dict(observed, observed_monotonic=time.monotonic())
                        # This private reference is never uploaded. The parent
                        # may make an independent Service read without extending
                        # any CloudForge command's observation deadline.
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


def check_report(report, code, case):
    evidence = {item["experiment_id"]: item for item in report["evidence"]}

    def require(condition, reason):
        if not condition:
            raise helpers.QualificationError(reason)
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
        samples = worker.get("samples", [])
        require(worker.get("pod_uid") and worker.get("key_absent_before_start") and worker.get("maximum_running_pods") == 1,
                "worker_single_writer_boundary_unproven")
        require(len(samples) == 2 and all(0 < sample["ttl_ms"] <= 60000 and 0 <= sample["age_ms"] <= 30000 for sample in samples),
                "worker_fresh_bounded_progress_unproven")
        require(datetime.fromisoformat(samples[1]["timestamp"].replace("Z", "+00:00")) > datetime.fromisoformat(samples[0]["timestamp"].replace("Z", "+00:00")),
                "worker_progress_timestamps_not_advancing")
        if name != "worker-startup":
            require(worker.get("previous_pod_uid") and worker["previous_pod_uid"] != worker["pod_uid"]
                    and worker.get("predecessor_terminated") and worker.get("previous_heartbeat_gone"),
                    "worker_predecessor_boundary_unproven")
            require(item.get("recovery", {}).get("status") == "pass", "healthy_worker_baseline_not_restored")
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
    elif case in ("never", "stale", "frozen", "exit"):
        require(code == 1 and report["status"] == "fail" and evidence["worker-startup"]["status"] == "fail", "valid_worker_requirement_failure_not_retained")
        require(not evidence["worker-recovery"].get("execution", {}).get("executed"), "worker_recovery_executed_without_startup")
    else:
        require(code == 1 and report["status"] == "fail", "worker_recovery_failure_disappeared")
        require(evidence["worker-startup"]["status"] == "pass" and evidence["worker-recovery"]["status"] == "fail", "old_worker_heartbeat_satisfied_new_process")
        if case == "fail-second-start":
            require(evidence["worker-recovery"].get("recovery", {}).get("status") == "pass", "worker_baseline_not_restored")
            require(evidence["worker-image-replacement"]["status"] == "pass" and evidence["worker-image-replacement"].get("execution", {}).get("executed"), "worker_did_not_continue_after_restoration")
        else:
            require(evidence["worker-recovery"].get("recovery", {}).get("status") != "pass", "worker_false_restoration")
            require(evidence["worker-image-replacement"]["status"] == "blocked" and not evidence["worker-image-replacement"].get("execution", {}).get("executed"), "worker_continued_without_restoration")


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
            if observations["observer_errors"]:
                raise helpers.QualificationError("worker_ownership_observation_failed")
            if case not in ("exit",) and not observations["pods"]:
                raise helpers.QualificationError("running_worker_not_independently_observed")
            for pod in observations["pods"]:
                if not all(pod[field] for field in ("no_http_probes", "no_container_ports")) or pod["replicas"] != 1 or pod["strategy"] != "Recreate":
                    raise helpers.QualificationError("worker_runtime_topology_mismatch")
            if case in ("healthy", "never", "stale", "frozen", "first-pod-only", "fail-second-start"):
                if not service_proof or not service_proof.get("observed") or not service_proof.get("no_application_service"):
                    raise helpers.QualificationError("worker_application_service_absence_unobserved")
            if case in ("healthy", "fail-second-start"):
                if len(observations["pods"]) < 5 or not any(pod["image"].endswith("-b") for pod in observations["pods"]):
                    raise helpers.QualificationError("worker_distinct_recovery_and_image_pods_unobserved")
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
