#!/usr/bin/env python3
"""Qualify cancellation of real backend phases on disposable GitHub runners.

The default is read-only analyze/plan. --run is guarded before runtime side
effects. A bounded public-fixture hold makes a running phase observable; SIGINT
is sent only after Kubernetes proves run ownership and container execution.
Neither Secret objects, application logs nor environment values are queried.
"""
import argparse
from contextlib import contextmanager
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


HOLD_SECONDS = 90
OBSERVATION_SECONDS = 600
VERIFY_SECONDS = 2400
CLEANUP_SECONDS = 240
MAX_OUTPUT = 256 * 1024
STAGES = ("clamav", "postgresql", "preparation")
PROVIDERS = {"clamav": "cf-dependency-clamav", "postgresql": "cf-dependency-postgres"}
ENV_PREFIX = "CF_BACKEND_CANCEL_"


class QualificationError(Exception):
    """Fixed, non-sensitive qualification failure code."""


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def require_runner(environment=None, platform=None):
    environment = os.environ if environment is None else environment
    platform = sys.platform if platform is None else platform
    if (environment.get("GITHUB_ACTIONS") != "true"
            or environment.get("RUNNER_ENVIRONMENT") != "github-hosted"
            or environment.get("RUNNER_OS") != "Linux" or platform != "linux"):
        raise QualificationError("runtime_requires_disposable_github_hosted_linux")


def capture(arguments, timeout, limit=MAX_OUTPUT):
    """Read both streams with hard memory/time bounds, never printing errors."""
    process = subprocess.Popen(arguments, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    streams = {process.stdout: bytearray(), process.stderr: bytearray()}
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selected:
            for stream in streams:
                selected.register(stream, selectors.EVENT_READ)
            while selected.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise QualificationError("observer_command_timeout")
                for key, _ in selected.select(min(remaining, 0.1)):
                    chunk = os.read(key.fileobj.fileno(), 8192)
                    if not chunk:
                        selected.unregister(key.fileobj)
                    elif len(streams[key.fileobj]) + len(chunk) > limit:
                        raise QualificationError("observer_output_limit")
                    else:
                        streams[key.fileobj].extend(chunk)
        code = process.wait(timeout=max(0.1, deadline - time.monotonic()))
        return subprocess.CompletedProcess(arguments, code, bytes(streams[process.stdout]), bytes(streams[process.stderr]))
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)
        for stream in streams:
            stream.close()


def context_identity(arguments):
    if "--context" not in arguments:
        raise QualificationError("observer_missing_isolated_context")
    context = arguments[arguments.index("--context") + 1]
    match = re.fullmatch(r"k3d-cloudforge-([a-z0-9][a-z0-9-]{0,80})", context)
    if not match:
        raise QualificationError("observer_unrelated_context")
    return context, match.group(1)


def metadata(obj, run_id, name=None, role=None):
    """Only allow safe object identity fields into qualification artifacts."""
    meta = obj.get("metadata", {})
    labels = meta.get("labels", {})
    actual_name, uid = meta.get("name", ""), meta.get("uid", "")
    if (labels.get("app.kubernetes.io/managed-by") != "cloudforge"
            or labels.get("cloudforge.dev/run-id") != run_id
            or meta.get("namespace") != "cloudforge" or meta.get("deletionTimestamp")
            or not re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", actual_name)
            or not re.fullmatch(r"[a-zA-Z0-9-]{1,80}", uid)
            or (name is not None and name != actual_name)
            or (role is not None and labels.get("cloudforge.dev/role") != role)):
        return None
    return {"name": actual_name, "uid": uid}


def controller(obj, kind, expected=None):
    owners = [o for o in obj.get("metadata", {}).get("ownerReferences", []) if o.get("controller") is True]
    if len(owners) != 1 or owners[0].get("kind") != kind:
        return None
    owner = owners[0]
    name, uid = owner.get("name", ""), owner.get("uid", "")
    if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", name) or not re.fullmatch(r"[a-zA-Z0-9-]{1,80}", uid):
        return None
    value = {"name": name, "uid": uid}
    return value if expected is None or value == expected else None


def running_container(statuses, name):
    matching = [s for s in statuses if s.get("name") == name]
    if len(matching) != 1 or matching[0].get("restartCount", 0) != 0:
        return None
    started = matching[0].get("state", {}).get("running", {}).get("startedAt", "")
    if not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z", started):
        return None
    return {"container": name, "started_at": started}


def provider_observation(stage, run_id, deployment, pods, replica_set):
    dep = metadata(deployment, run_id, PROVIDERS[stage])
    items = pods.get("items", [])
    if not dep or len(items) != 1:
        return None
    pod = items[0]
    pod_id, rs = metadata(pod, run_id), metadata(replica_set, run_id)
    if (not pod_id or not rs or controller(pod, "ReplicaSet", rs) is None
            or controller(replica_set, "Deployment", dep) is None
            or pod.get("metadata", {}).get("labels", {}).get("cloudforge.dev/dependency") != stage):
        return None
    if any(c.get("type") == "Ready" and c.get("status") == "True" for c in pod.get("status", {}).get("conditions", [])):
        return None
    status = pod.get("status", {})
    if stage == "clamav":
        running = running_container(status.get("initContainerStatuses", []), "update-signatures")
        phase = "signature_update"
        if not running:
            updated = [s for s in status.get("initContainerStatuses", []) if s.get("name") == "update-signatures"]
            if len(updated) != 1 or updated[0].get("state", {}).get("terminated", {}).get("exitCode") != 0:
                return None
            running = running_container(status.get("containerStatuses", []), "clamav")
            phase = "daemon_startup_after_signature_update"
    else:
        running = running_container(status.get("containerStatuses", []), "postgres")
        phase = "postgresql_startup"
    if not running:
        return None
    return {"stage": stage, "run_id": run_id, "deployment": dep, "replica_set": rs,
            "pod": pod_id, "observed_phase": phase, **running,
            "signature_update_observed_running": stage == "clamav" and phase == "signature_update"}


def preparation_observation(run_id, job_identity, pods):
    items = pods.get("items", [])
    if len(items) != 1:
        return None
    pod = items[0]
    pod_id = metadata(pod, run_id, role="preparation")
    running = running_container(pod.get("status", {}).get("containerStatuses", []), "preparation")
    if not pod_id or not running or controller(pod, "Job", job_identity) is None:
        return None
    return {"stage": "preparation", "run_id": run_id, "job": job_identity, "pod": pod_id,
            "observed_phase": "preparation_command_running", **running}


def delay_probes(data):
    changed, count = re.subn(r"(?m)^(\s*)(startupProbe|readinessProbe):\s*$",
                             lambda m: m.group(0) + "\n" + m.group(1) + "  initialDelaySeconds: " + str(HOLD_SECONDS), data)
    if count != 2 or "initialDelaySeconds:" in data:
        raise QualificationError("unexpected_provider_probe_shape")
    return changed


def publish_marker(value):
    marker = Path(os.environ[ENV_PREFIX + "MARKER"])
    if marker.exists():
        return
    value = dict(value, observed_monotonic=time.monotonic())
    pending = marker.with_suffix(".pending")
    write_json(pending, value)
    pending.replace(marker)


def query(real, context, *arguments):
    result = capture([real, "--context", context, "--namespace", "cloudforge", "--request-timeout=2s", *arguments, "--output=json"], 3)
    if result.returncode:
        return None
    try:
        value = json.loads(result.stdout)
    except (ValueError, UnicodeError):
        raise QualificationError("observer_invalid_json") from None
    if not isinstance(value, dict):
        raise QualificationError("observer_invalid_object")
    return value


def provider_wait(real, arguments, stage):
    context, run_id = context_identity(arguments)
    process = subprocess.Popen([real, *arguments])
    deadline = time.monotonic() + OBSERVATION_SECONDS
    try:
        while process.poll() is None and time.monotonic() < deadline:
            deployment = query(real, context, "get", "deployment", PROVIDERS[stage])
            pods = query(real, context, "get", "pods", "--selector="
                         + f"app.kubernetes.io/managed-by=cloudforge,cloudforge.dev/run-id={run_id},cloudforge.dev/dependency={stage}")
            if deployment and pods and len(pods.get("items", [])) == 1:
                owner = controller(pods["items"][0], "ReplicaSet")
                rs = query(real, context, "get", "replicaset", owner["name"]) if owner else None
                observation = provider_observation(stage, run_id, deployment, pods, rs) if rs else None
                if observation:
                    publish_marker(observation)
                    return process.wait(timeout=15)
            time.sleep(0.1)
        if process.poll() is None:
            raise QualificationError("provider_start_not_observed_within_bound")
        return process.returncode
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)


def kubectl_wrapper(arguments):
    require_runner()
    stage, real = os.environ[ENV_PREFIX + "STAGE"], os.environ[ENV_PREFIX + "REAL_KUBECTL"]
    if stage not in STAGES:
        raise QualificationError("invalid_stage")
    if stage in PROVIDERS and "apply" in arguments and "--filename" in arguments:
        path = Path(arguments[arguments.index("--filename") + 1])
        if path.name == "dependency-" + stage + ".yaml":
            context_identity(arguments)
            if not path.parent.name.startswith("cloudforge-verify-") or path.is_symlink():
                raise QualificationError("fixture_hold_outside_owned_workspace")
            path.write_text(delay_probes(path.read_text()))
    if stage in PROVIDERS and "rollout" in arguments and "status" in arguments and "deployment/" + PROVIDERS[stage] in arguments:
        return provider_wait(real, arguments, stage)
    is_job = "get" in arguments and "job" in arguments and "cf-preparation" in arguments
    is_pods = "get" in arguments and "pods" in arguments and "--selector=batch.kubernetes.io/job-name=cf-preparation" in arguments
    if stage != "preparation" or not (is_job or is_pods):
        return subprocess.call([real, *arguments])
    _, run_id = context_identity(arguments)
    state_path = Path(os.environ[ENV_PREFIX + "STATE"])
    state = json.loads(state_path.read_text()) if state_path.exists() else {}
    # The next Job observation proves CloudForge consumed the previous running
    # Pod observation and set execution.executed before receiving cancellation.
    if is_job and state.get("candidate") and time.monotonic() - state.get("candidate_time", 0) <= 5:
        publish_marker(dict(state["candidate"], cloudforge_consumed_running_observation=True))
    result = capture([real, *arguments], 5.5)
    if result.returncode == 0:
        data = json.loads(result.stdout)
        if is_job:
            identity = metadata(data, run_id, "cf-preparation", "preparation")
            state = {"job": identity} if identity else {}
        elif state.get("job"):
            value = preparation_observation(run_id, state["job"], data)
            if value:
                state.update(candidate=value, candidate_time=time.monotonic())
        write_json(state_path, state)
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)
    return result.returncode


def hashes(directory):
    return {str(p.relative_to(directory)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(directory.rglob("*")) if p.is_file()}


def fixture_copy(source, target, stage):
    shutil.copytree(source, target)
    hold = {"seconds": HOLD_SECONDS, "scope": "public_fixture_copy_only"}
    if stage == "preparation":
        script = target / "prepare.js"
        text = script.read_text()
        needle = "async function main() {"
        if text.count(needle) != 1:
            raise QualificationError("unexpected_preparation_fixture_shape")
        script.write_text(text.replace(needle, needle + f"\n  await new Promise(resolve => setTimeout(resolve, {HOLD_SECONDS * 1000}));"))
        config = json.loads((target / "cloudforge.yaml").read_text())
        config["preparation"]["timeout"] = "120s"
        write_json(target / "cloudforge.yaml", config)
        hold["kind"] = "preparation_command_running_before_schema_operations"
    else:
        hold["kind"] = "initial_probe_delay_in_run_owned_provider_manifest"
        hold["provider"] = stage
    return hold


def inspect(binary, fixture, output):
    for operation in ("analyze", "verify"):
        command = [binary, operation, str(fixture), "--format", "json"]
        if operation == "verify":
            command.append("--plan")
        first, second = capture(command, 30), capture(command, 30)
        (output / (operation + ".stdout.json")).write_bytes(first.stdout)
        (output / (operation + ".stderr.txt")).write_bytes(first.stderr)
        if first.returncode or second.returncode or first.stdout != second.stdout:
            raise QualificationError("read_only_plan_failed_or_nondeterministic")
        if json.loads(first.stdout).get("status") != "pass":
            raise QualificationError("read_only_plan_not_supported")


def docker(*arguments, timeout=30):
    result = capture(["docker", *arguments], timeout)
    if result.returncode:
        raise QualificationError("unrelated_state_setup_or_observation_failed")
    return result.stdout.decode().strip()


@contextmanager
def existing_state(image):
    """Create unrelated sentinels only inside the guarded disposable runner."""
    name = "pilot-existing-backend-" + str(os.getpid())
    kubeconfig = Path.home() / ".kube" / "config"
    if kubeconfig.is_symlink():
        raise QualificationError("runner_kubeconfig_is_symlink")
    original = kubeconfig.read_bytes() if kubeconfig.exists() else None
    original_mode = kubeconfig.stat().st_mode & 0o777 if original is not None else None
    created = []
    try:
        # Match the backend profile's local daemon contract before creating even
        # unrelated sentinels. Never let a saved remote context receive them.
        if (os.environ.get("DOCKER_HOST", "unix:///var/run/docker.sock") != "unix:///var/run/docker.sock"
                or os.environ.get("DOCKER_CONTEXT", "default") != "default"
                or docker("context", "show") != "default"):
            raise QualificationError("qualification_requires_default_local_docker")
        try:
            endpoint = json.loads(docker("context", "inspect", "default"))[0]["Endpoints"]["docker"]["Host"]
        except (IndexError, KeyError, TypeError, ValueError):
            raise QualificationError("local_docker_endpoint_unobservable") from None
        if endpoint != "unix:///var/run/docker.sock":
            raise QualificationError("qualification_requires_default_local_docker")
        docker("network", "create", name)
        created.append("network")
        docker("volume", "create", name)
        created.append("volume")
        docker("run", "--detach", "--name", name, "--network", name, "--volume", name + ":/data",
               "--memory", "32m", "--cpus", "0.1", "--read-only", "--cap-drop", "ALL",
               "--security-opt", "no-new-privileges", image, "/bin/sleep", "3600", timeout=180)
        created.append("container")
        kubeconfig.parent.mkdir(parents=True, exist_ok=True)
        sentinel = b"apiVersion: v1\nkind: Config\ncurrent-context: existing\nclusters: []\ncontexts: []\nusers: []\n"
        kubeconfig.write_bytes(sentinel)
        kubeconfig.chmod(0o600)
        identities = {kind: docker(kind, "inspect", "--format", "{{.Name}}" if kind == "volume" else "{{.Id}}", name) for kind in created}
        yield {"name": name, "identities": identities, "kubeconfig": kubeconfig, "sentinel": sentinel}
    finally:
        for kind in reversed(created):
            command = ["docker", kind, "rm", *(["--force"] if kind == "container" else []), name]
            try:
                capture(command, 30)
            except (OSError, QualificationError, subprocess.SubprocessError):
                pass
        if original is None:
            kubeconfig.unlink(missing_ok=True)
        else:
            kubeconfig.write_bytes(original)
            kubeconfig.chmod(original_mode)


def preserve_cleanup(repository, output, private, state):
    result = capture(["bash", str(repository / "scripts/pilot-cleanup-check.sh")], 30)
    (output / "cleanup.stdout.txt").write_bytes(result.stdout)
    (output / "cleanup.stderr.txt").write_bytes(result.stderr)
    proofs = {"owned_resource_check_exit_code": result.returncode,
              "private_runtime_workspaces_removed": not list(private.glob("cloudforge-verify-*")),
              "user_kubeconfig_unchanged": state["kubeconfig"].exists() and state["kubeconfig"].read_bytes() == state["sentinel"],
              "unrelated_resources_unchanged": {}}
    for kind, identity in state["identities"].items():
        try:
            observed = docker(kind, "inspect", "--format", "{{.Name}}" if kind == "volume" else "{{.Id}}", state["name"])
            proofs["unrelated_resources_unchanged"][kind] = identity == observed
        except QualificationError:
            proofs["unrelated_resources_unchanged"][kind] = False
    try:
        proofs["unrelated_container_still_running"] = docker("container", "inspect", "--format", "{{.State.Running}}", state["name"]) == "true"
    except QualificationError:
        proofs["unrelated_container_still_running"] = False
    proofs["passed"] = (result.returncode == 0 and proofs["private_runtime_workspaces_removed"]
                        and proofs["user_kubeconfig_unchanged"] and proofs["unrelated_container_still_running"]
                        and all(proofs["unrelated_resources_unchanged"].values()))
    write_json(output / "cleanup.json", proofs)
    return proofs


def report_checks(report, stage):
    evidence = {e["experiment_id"]: e for e in report.get("evidence", [])}
    target = "application-preparation" if stage == "preparation" else "dependency." + stage
    item = evidence.get(target, {})
    checks = {"schema_version": report.get("schema_version"),
              "producer_identified": report.get("producer", {}).get("commit") not in (None, "", "unknown")
              and report.get("producer", {}).get("version") not in (None, "", "unknown"),
              "overall_error": report.get("status") == "error",
              "cancellation_diagnostic": any(d.get("code") == "verification_canceled" for d in report.get("diagnostics", [])),
              "target_executed": item.get("execution", {}).get("executed") is True,
              "target_error": item.get("status") == "error",
              "application_not_started": not evidence.get("deployment-readiness", {}).get("execution", {}).get("executed", False)}
    if stage in PROVIDERS:
        checks["dependency_error"] = any(d.get("name") == stage and d.get("status") == "error" for d in report.get("dependencies", []))
    checks["passed"] = checks["schema_version"] == "v1alpha6" and all(v is True for k, v in checks.items() if k != "schema_version")
    return checks


def runtime(binary, fixture, output, repository, private, stage, qualification):
    real = shutil.which("kubectl")
    if not real:
        raise QualificationError("kubectl_missing")
    image_match = re.search(r"(?m)^FROM (\S+@sha256:[a-f0-9]{64})$", (fixture / "Dockerfile").read_text())
    if not image_match:
        raise QualificationError("sentinel_requires_pinned_public_fixture_image")
    wrapper_dir = private / "bin"
    wrapper_dir.mkdir()
    wrapper = wrapper_dir / "kubectl"
    wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable,os.environ['CF_BACKEND_CANCEL_HARNESS'],'__kubectl',*sys.argv[1:]])\n")
    wrapper.chmod(0o700)
    marker = private / "observed-start.json"
    environment = dict(os.environ, TMPDIR=str(private), PATH=str(wrapper_dir) + os.pathsep + os.environ["PATH"])
    environment.update({ENV_PREFIX + "HARNESS": str(Path(__file__).resolve()), ENV_PREFIX + "REAL_KUBECTL": real,
                        ENV_PREFIX + "STAGE": stage, ENV_PREFIX + "MARKER": str(marker), ENV_PREFIX + "STATE": str(private / "preparation-state.json")})
    command = [binary, "verify", str(fixture), "--format", "json"]
    write_json(output / "command.json", command)
    with existing_state(image_match.group(1)) as state:
        process = None
        interrupted_at = None
        try:
            with (output / "stdout.json").open("wb") as stdout, (output / "stderr.txt").open("wb") as stderr:
                process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=environment)
                qualification["runtime_executed"] = True
                deadline = time.monotonic() + VERIFY_SECONDS
                while process.poll() is None and not marker.exists() and time.monotonic() < deadline:
                    time.sleep(0.1)
                if not marker.exists():
                    raise QualificationError("owned_running_target_not_observed")
                observed = json.loads(marker.read_text())
                if process.poll() is not None or time.monotonic() - observed["observed_monotonic"] > 5:
                    raise QualificationError("target_ended_or_observation_stale")
                qualification["interruption"] = observed
                qualification["interruption"]["proof_scope"] = "cancellation_after_owned_running_container_observed"
                # Kubernetes state is sampled: a short init command can finish
                # between observation and SIGINT. Do not claim atomic knowledge
                # of its exact subphase at signal delivery.
                qualification["interruption"]["phase_completion_at_signal"] = "not_observed"
                interrupted_at = time.monotonic()
                process.send_signal(signal.SIGINT)
                qualification["signal_sent"] = "SIGINT"
                process.wait(timeout=CLEANUP_SECONDS)
            write_json(output / "exit.json", {"exit_code": process.returncode})
            checks = report_checks(json.loads((output / "stdout.json").read_text()), stage)
            qualification["report_checks"] = checks
            if process.returncode != 2 or not checks["passed"]:
                raise QualificationError("cancellation_report_contract_failed")
            for format_name, file_name in (("json", "canonical.json"), ("markdown", "report.md")):
                result = capture([binary, "report", str(output / "stdout.json"), "--format", format_name], 30, 4 * 1024 * 1024)
                (output / file_name).write_bytes(result.stdout)
                (output / (file_name + ".stderr.txt")).write_bytes(result.stderr)
                if result.returncode:
                    raise QualificationError("cancellation_report_reload_failed")
        finally:
            if process is not None and process.poll() is None:
                # An aborted qualification still gives CloudForge bounded cleanup
                # time. This fallback is never counted as a qualified stage signal.
                if interrupted_at is None:
                    interrupted_at = time.monotonic()
                    process.send_signal(signal.SIGINT)
                    qualification["fallback_cleanup_signal"] = "SIGINT"
                try:
                    process.wait(timeout=max(0, CLEANUP_SECONDS - (time.monotonic() - interrupted_at)))
                except subprocess.TimeoutExpired:
                    qualification["forced_kill"] = True
                    process.kill()
                    process.wait(timeout=10)
            if process is not None:
                write_json(output / "exit.json", {"exit_code": process.returncode})
            qualification["cleanup"] = preserve_cleanup(repository, output, private, state)
    if not qualification["cleanup"]["passed"]:
        raise QualificationError("cleanup_or_unrelated_state_preservation_failed")


def self_test():
    """Pure/injected checks: no Docker, Kubernetes, fixture execution or network."""
    class Tests(unittest.TestCase):
        def test_guard(self):
            good = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux"}
            require_runner(good, "linux")
            for environment, platform in (({}, "linux"), (dict(good, RUNNER_ENVIRONMENT="self-hosted"), "linux"), (good, "darwin")):
                with self.assertRaises(QualificationError):
                    require_runner(environment, platform)
            with tempfile.TemporaryDirectory() as temporary:
                target = Path(temporary) / "not-created"
                environment = dict(os.environ, GITHUB_ACTIONS="false")
                result = subprocess.run([sys.executable, __file__, "/invalid-binary", str(target), "--run", "--stage", "clamav"],
                                        env=environment, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 2)
                self.assertFalse(target.exists())

        def test_owned_running_observations(self):
            def obj(name, uid, owner=None):
                value = {"metadata": {"name": name, "uid": uid, "namespace": "cloudforge", "labels": {
                    "app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": "test-run",
                    "cloudforge.dev/dependency": "clamav", "cloudforge.dev/role": "preparation"}}}
                if owner:
                    value["metadata"]["ownerReferences"] = [dict(owner, controller=True)]
                return value
            dep = obj("cf-dependency-clamav", "dep-uid")
            rs = obj("cf-dependency-clamav-rs", "rs-uid", {"kind": "Deployment", "name": "cf-dependency-clamav", "uid": "dep-uid"})
            pod = obj("cf-dependency-clamav-pod", "pod-uid", {"kind": "ReplicaSet", "name": "cf-dependency-clamav-rs", "uid": "rs-uid"})
            started = {"running": {"startedAt": "2026-09-21T10:00:00Z"}}
            pod["status"] = {"initContainerStatuses": [{"name": "update-signatures", "state": started, "restartCount": 0}]}
            call = lambda: provider_observation("clamav", "test-run", dep, {"items": [pod]}, rs)
            result = call()
            self.assertTrue(result["signature_update_observed_running"])
            pod["spec"] = {"env": "SECRET-CANARY"}
            pod["status"]["message"] = "SECRET-CANARY"
            self.assertNotIn("SECRET-CANARY", json.dumps(call()))
            pod["metadata"]["ownerReferences"][0]["uid"] = "unrelated"
            self.assertIsNone(call())
            pod["metadata"]["ownerReferences"][0]["uid"] = "rs-uid"
            pod["status"]["initContainerStatuses"][0]["state"] = {"terminated": {"exitCode": 0}}
            self.assertIsNone(call())
            pod["status"]["containerStatuses"] = [{"name": "clamav", "state": started}]
            self.assertFalse(call()["signature_update_observed_running"])
            pod["status"]["conditions"] = [{"type": "Ready", "status": "True"}]
            self.assertIsNone(call())
            pg_dep = obj("cf-dependency-postgres", "pg-dep")
            pg_rs = obj("cf-dependency-postgres-rs", "pg-rs", {"kind": "Deployment", "name": "cf-dependency-postgres", "uid": "pg-dep"})
            pg_pod = obj("cf-dependency-postgres-pod", "pg-pod", {"kind": "ReplicaSet", "name": "cf-dependency-postgres-rs", "uid": "pg-rs"})
            pg_pod["metadata"]["labels"]["cloudforge.dev/dependency"] = "postgresql"
            pg_pod["status"] = {"containerStatuses": [{"name": "postgres", "state": started}]}
            self.assertIsNotNone(provider_observation("postgresql", "test-run", pg_dep, {"items": [pg_pod]}, pg_rs))
            pg_pod["metadata"]["labels"]["cloudforge.dev/run-id"] = "another-run"
            self.assertIsNone(provider_observation("postgresql", "test-run", pg_dep, {"items": [pg_pod]}, pg_rs))
            job_id = {"name": "cf-preparation", "uid": "job-uid"}
            preparation = obj("cf-preparation-pod", "prepare-uid", dict(job_id, kind="Job"))
            preparation["status"] = {"containerStatuses": [{"name": "preparation", "state": started}]}
            self.assertIsNotNone(preparation_observation("test-run", job_id, {"items": [preparation]}))
            preparation["status"]["containerStatuses"][0]["state"] = {"waiting": {}}
            self.assertIsNone(preparation_observation("test-run", job_id, {"items": [preparation]}))

        def test_probe_hold(self):
            fixture = "    startupProbe:\n      periodSeconds: 2\n    readinessProbe:\n      periodSeconds: 2\n"
            self.assertEqual(delay_probes(fixture).count("initialDelaySeconds: 90"), 2)
            with self.assertRaises(QualificationError):
                delay_probes("startupProbe: {}")
            with self.assertRaises(QualificationError):
                delay_probes(delay_probes(fixture))

        def test_bounded_capture(self):
            result = capture([sys.executable, "-c", "import sys;print('ok');print('err',file=sys.stderr)"], 5)
            self.assertEqual((result.stdout, result.stderr), (b"ok\n", b"err\n"))
            with self.assertRaisesRegex(QualificationError, "observer_output_limit"):
                capture([sys.executable, "-c", "print('x'*300000)"], 5)
            with self.assertRaisesRegex(QualificationError, "observer_command_timeout"):
                capture([sys.executable, "-c", "import time;time.sleep(1)"], 0.05)

        def test_preparation_marker_requires_consumed_running_observation(self):
            labels = {"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": "test-run", "cloudforge.dev/role": "preparation"}
            job = {"metadata": {"name": "cf-preparation", "uid": "job-uid", "namespace": "cloudforge", "labels": labels}}
            pod = {"metadata": {"name": "cf-preparation-pod", "uid": "pod-uid", "namespace": "cloudforge", "labels": labels,
                                "ownerReferences": [{"kind": "Job", "name": "cf-preparation", "uid": "job-uid", "controller": True}]},
                   "status": {"containerStatuses": [{"name": "preparation", "state": {"running": {"startedAt": "2026-09-21T10:00:00Z"}}}]}}
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_json(root / "job.json", job)
                write_json(root / "pods.json", {"items": [pod]})
                fake = root / "fake-kubectl"
                fake.write_text("#!/usr/bin/env python3\nimport pathlib,sys\nroot=pathlib.Path(__file__).parent\nprint((root/('pods.json' if 'pods' in sys.argv else 'job.json')).read_text())\n")
                fake.chmod(0o700)
                marker = root / "marker.json"
                environment = dict(os.environ, GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted", RUNNER_OS="Linux")
                environment.update({ENV_PREFIX + "STAGE": "preparation", ENV_PREFIX + "REAL_KUBECTL": str(fake),
                                    ENV_PREFIX + "MARKER": str(marker), ENV_PREFIX + "STATE": str(root / "state.json")})
                prefix = [sys.executable, __file__, "__kubectl", "--context", "k3d-cloudforge-test-run", "--namespace", "cloudforge"]
                for arguments in (("get", "job", "cf-preparation"), ("get", "pods", "--selector=batch.kubernetes.io/job-name=cf-preparation")):
                    result = subprocess.run([*prefix, *arguments], env=environment, capture_output=True, timeout=5)
                    self.assertEqual(result.returncode, 0)
                    self.assertFalse(marker.exists(), "marker preceded consumption of the running Pod observation")
                result = subprocess.run([*prefix, "get", "job", "cf-preparation"], env=environment, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 0)
                self.assertTrue(json.loads(marker.read_text())["cloudforge_consumed_running_observation"])

        def test_canceled_report_cannot_fake_completed_evidence(self):
            report = {"schema_version": "v1alpha6", "status": "error", "producer": {"version": "test", "commit": "abc123"},
                      "diagnostics": [{"code": "verification_canceled"}], "evidence": [
                          {"experiment_id": "application-preparation", "status": "error", "execution": {"executed": True}},
                          {"experiment_id": "deployment-readiness", "status": "skipped", "execution": {"executed": False}}]}
            self.assertTrue(report_checks(report, "preparation")["passed"])
            bad = copy.deepcopy(report)
            bad["evidence"][0]["execution"]["executed"] = False
            self.assertFalse(report_checks(bad, "preparation")["passed"])
            bad = copy.deepcopy(report)
            bad["evidence"][1]["execution"]["executed"] = True
            self.assertFalse(report_checks(bad, "preparation")["passed"])

        def test_apply_is_not_start_evidence(self):
            # Merely spawning or succeeding at apply must not signal a running
            # phase, even if provider objects were accepted by the API.
            with tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / "cloudforge-verify-test"
                path.mkdir()
                manifest = path / "dependency-clamav.yaml"
                manifest.write_text("startupProbe:\n  periodSeconds: 2\nreadinessProbe:\n  periodSeconds: 2\n")
                environment = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux",
                               ENV_PREFIX + "STAGE": "clamav", ENV_PREFIX + "REAL_KUBECTL": "fake-kubectl"}
                with patch.dict(os.environ, environment), patch("subprocess.call", return_value=0), patch(__name__ + ".publish_marker") as marker:
                    self.assertEqual(kubectl_wrapper(["--context", "k3d-cloudforge-test", "apply", "--filename", str(manifest)]), 0)
                    marker.assert_not_called()

    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(Tests))
    return 0 if result.wasSuccessful() else 1


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "__kubectl":
        try:
            return kubectl_wrapper(sys.argv[2:])
        except (OSError, ValueError, TypeError, AttributeError, IndexError, KeyError, QualificationError, subprocess.SubprocessError):
            print("backend cancellation observer could not establish bounded evidence", file=sys.stderr)
            return 2
    if sys.argv[1:] == ["--self-test"]:
        return self_test()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("output", type=Path)
    parser.add_argument("--stage", choices=STAGES, required=True)
    parser.add_argument("--run", action="store_true", help="Execute only on a disposable GitHub-hosted Linux runner")
    args = parser.parse_args()
    if args.run:
        require_runner()  # Must precede output directories, runtime tools or kubeconfig access.
    repository = Path(__file__).resolve().parent.parent
    source = repository / "testdata/backend-http"
    before = hashes(source)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    qualification = {"schema_version": "v1", "stage": args.stage, "runtime_executed": False, "qualified": False,
                     "secret_objects_queried": False, "application_logs_queried": False,
                     "bounds_seconds": {"observer": OBSERVATION_SECONDS, "verification": VERIFY_SECONDS, "cleanup": CLEANUP_SECONDS},
                     "source_hashes": before, "raw_cli_files": ["stdout.json", "stderr.txt", "exit.json"]}
    error = None
    try:
        with tempfile.TemporaryDirectory(prefix="cf-backend-cancellation-") as temporary:
            private = Path(temporary)
            fixture = private / "fixture"
            qualification["fixture_hold"] = fixture_copy(source, fixture, args.stage)
            qualification["fixture_copy_hashes"] = hashes(fixture)
            binary = str(Path(args.binary).resolve())
            inspect(binary, fixture, output)
            qualification["read_only_plan_passed"] = True
            if args.run:
                runtime(binary, fixture, output, repository, private, args.stage, qualification)
                qualification["qualified"] = True
    except (OSError, ValueError, QualificationError, subprocess.SubprocessError) as exc:
        error = str(exc) if isinstance(exc, QualificationError) else "qualification_execution_error"
        qualification["failure"] = error
    finally:
        qualification["original_fixture_unchanged"] = hashes(source) == before
        if not qualification["original_fixture_unchanged"]:
            error = qualification["failure"] = "original_fixture_changed"
        if error:
            qualification["qualified"] = False
        write_json(output / "qualification.json", qualification)
    if error:
        print(error, file=sys.stderr)
        return 1
    print("backend cancellation qualified" if args.run else "read-only backend cancellation plan checked; runtime not executed")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except QualificationError as failure:
        print(str(failure), file=sys.stderr)
        sys.exit(2)
