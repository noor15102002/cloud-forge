#!/usr/bin/env python3
"""Qualify the generic backend contract, retaining exact CloudForge CLI output.

Without --run, only static fixture checks and deterministic analyze/plan run.
Runtime execution requires a disposable GitHub-hosted runner. The kubectl wrapper
adds public-fixture qualification probes, not application behavior or verdicts.
"""
import argparse
import base64
import copy
import hashlib
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
from urllib.parse import quote


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def kubectl_wrapper(arguments):
    real = os.environ["CF_BACKEND_REAL_KUBECTL"]
    result = subprocess.run([real, *arguments], check=False)
    if result.returncode or "apply" not in arguments or "--filename" not in arguments:
        return result.returncode
    manifest = Path(arguments[arguments.index("--filename") + 1]).name
    if "--context" not in arguments:
        raise RuntimeError("fixture wrapper requires an explicit isolated context")
    context = arguments[arguments.index("--context") + 1]
    match = re.fullmatch(r"k3d-cloudforge-([a-z0-9-]+)", context)
    if not match:
        raise RuntimeError("fixture wrapper refused an unrelated context")
    run_id = match.group(1)
    prefix = [real, "--context", context, "--namespace", "cloudforge"]

    def kube(*args, payload=None, timeout=15):
        call = subprocess.run([*prefix, *args], input=None if payload is None else json.dumps(payload),
                              capture_output=True, text=True, timeout=timeout, check=True)
        return call.stdout

    if manifest == "test-configuration.yaml":
        # Actual random run values are kept only in the private temporary folder,
        # checked against public output, then destroyed. No credentials are logged.
        secrets = json.loads(kube("get", "secrets", "--selector",
                                  f"app.kubernetes.io/managed-by=cloudforge,cloudforge.dev/run-id={run_id}", "--output", "json"))
        values = []
        for secret in secrets["items"]:
            for key, encoded in secret.get("data", {}).items():
                if key in ("password", "APP_TEST_KEY"):
                    raw = base64.b64decode(encoded).decode("utf-8")
                    if len(raw) < 16:
                        raise RuntimeError("generated fixture credential is unexpectedly short")
                    values.extend((raw, encoded, quote(raw, safe="")))
        path = Path(os.environ["CF_BACKEND_SECRET_CANARIES"])
        path.write_text(json.dumps(sorted(set(values))))
        path.chmod(0o600)

    if manifest not in ("network-startup.yaml", "network-runtime.yaml"):
        return 0
    output = Path(os.environ["CF_BACKEND_CASE_OUTPUT"])
    policy = json.loads(kube("get", "networkpolicy", "cf-egress-clamav-signatures", "--output", "json"))
    write_json(output / manifest.replace(".yaml", ".readback.json"), policy)
    if manifest == "network-startup.yaml":
        rules = policy["spec"].get("egress", [])
        if not rules or any(p.get("protocol") != "TCP" or p.get("port") not in (80, 443)
                            for rule in rules for p in rule.get("ports", [])):
            raise RuntimeError("signature startup policy did not expose bounded HTTP(S)")
        return 0
    if policy["spec"].get("egress"):
        raise RuntimeError("signature download allowance was not revoked")
    if os.environ.get("CF_BACKEND_NETWORK_PROBES") != "true":
        return 0
    qualify_network(kube, run_id, output)
    return 0


def qualify_network(kube, run_id, output):
    """Test real CNI filtering against a live controlled, private canary.

    Public signature egress revocation is a policy-readback claim only. This
    deliberately makes no internet-connectivity or arbitrary-host-sandbox claim.
    """
    owned = {"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": run_id}
    names = {name: "cf-backend-probe-" + name for name in ("canary", "control", "application", "preparation", "clamav")}
    image = f"cloudforge/backend-http:{run_id}-a"
    objects = []
    for name, pod_name in names.items():
        labels = dict(owned, **{"cloudforge.dev/qualification": name})
        if name in ("application", "preparation"):
            labels["cloudforge.dev/role"] = name
        if name == "clamav":
            labels["cloudforge.dev/dependency"] = "clamav"
        container = {"name": "probe", "image": image, "imagePullPolicy": "Never",
                     "command": ["node", "network-probe.js", "listen" if name == "canary" else "idle"],
                     "resources": {"requests": {"cpu": "25m", "memory": "32Mi"}, "limits": {"cpu": "100m", "memory": "64Mi"}},
                     "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "allowPrivilegeEscalation": False,
                                         "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}}}
        if name == "canary":
            container["readinessProbe"] = {"tcpSocket": {"port": 8081}, "periodSeconds": 1}
        objects.append({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": pod_name, "labels": labels},
                        "spec": {"restartPolicy": "Never", "automountServiceAccountToken": False,
                                 "terminationGracePeriodSeconds": 1, "containers": [container]}})
    control_policy = "cf-backend-qualification-control"
    objects.append({"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
                    "metadata": {"name": control_policy, "labels": owned},
                    "spec": {"podSelector": {"matchLabels": dict(owned, **{"cloudforge.dev/qualification": "control"})},
                             "policyTypes": ["Egress"],
                             "egress": [{"to": [{"podSelector": {"matchLabels": dict(owned, **{"cloudforge.dev/qualification": "canary"})}}],
                                         "ports": [{"protocol": "TCP", "port": 8081}]}]}})
    observations = {"scope": "controlled_private_canary_on_actual_cni", "startup_public_egress_tested": False,
                    "public_allowance_revocation": "policy_readback_only", "checks": [], "cleanup": False}
    try:
        kube("apply", "--filename", "-", payload={"apiVersion": "v1", "kind": "List", "items": objects})
        kube("wait", "--for=condition=Ready", *["pod/" + n for n in names.values()], "--timeout=20s", timeout=23)
        canary = json.loads(kube("get", "pod", names["canary"], "--output", "json"))["status"]["podIP"]

        def connect(role, host, port):
            return json.loads(kube("exec", names[role], "--", "node", "network-probe.js", "tcp", host, str(port), timeout=5))["connected"]

        deadline = time.monotonic() + 5
        while not connect("control", canary, 8081):
            if time.monotonic() >= deadline:
                raise RuntimeError("positive network control could not reach the live canary")
        observations["checks"].append({"name": "live_control_before", "passed": True})
        for role in ("application", "preparation", "clamav"):
            denied = not connect(role, canary, 8081)
            observations["checks"].append({"name": role + "_undeclared_destination_denied", "passed": denied})
            if not denied:
                raise RuntimeError("runtime CNI allowed an undeclared fixture destination")
        for name, service, port in (("postgresql", "cf-dependency-postgres", 5432), ("redis", "cf-dependency-redis", 6379), ("clamav", "cf-dependency-clamav", 3310)):
            allowed = connect("application", f"{service}.cloudforge.svc.cluster.local", port)
            observations["checks"].append({"name": name + "_declared_destination_allowed", "passed": allowed})
            if not allowed:
                raise RuntimeError("runtime CNI blocked a declared fixture provider")
        alive = connect("control", canary, 8081)
        observations["checks"].append({"name": "live_control_after", "passed": alive})
        if not alive:
            raise RuntimeError("network deny observations lost their live positive control")
    finally:
        # A denied connection only means isolation while a positive control is
        # alive. Retain bounded container state before deleting the canary so a
        # helper crash is distinguishable from CNI denial; never fetch pod logs.
        pending_exception = sys.exc_info()[0] is not None
        try:
            pod = json.loads(kube("get", "pod", names["canary"], "--output", "json", timeout=5))
            status = pod.get("status", {})
            containers = []
            for container in status.get("containerStatuses", []):
                item = {"name": container["name"], "ready": container.get("ready", False),
                        "restart_count": container.get("restartCount", 0)}
                for field, target in (("state", "state"), ("lastState", "previous_state")):
                    state = container.get(field, {})
                    for kind in ("running", "waiting", "terminated"):
                        if kind in state:
                            item[target] = {"kind": kind}
                            if kind == "terminated":
                                item[target].update(exit_code=state[kind].get("exitCode"), signal=state[kind].get("signal"))
                            break
                containers.append(item)
            observations["canary_state_before_cleanup"] = {"observed": True, "phase": status.get("phase"),
                                                           "containers": containers, "logs_collected": False}
        except Exception as error:
            observations["canary_state_before_cleanup"] = {"observed": False, "error_type": type(error).__name__,
                                                           "logs_collected": False}
        cleanup_errors = []
        for resource, targets in (("pod", list(names.values())), ("networkpolicy", [control_policy])):
            try:
                kube("delete", resource, *targets, "--ignore-not-found", "--wait=true", "--timeout=10s", timeout=13)
            except Exception as error:
                cleanup_errors.append({"resource": resource, "error_type": type(error).__name__})
        observations["cleanup"] = not cleanup_errors
        if cleanup_errors:
            observations["cleanup_errors"] = cleanup_errors
        write_json(output / "network-enforcement.json", observations)
        if cleanup_errors and not pending_exception:
            raise RuntimeError("network qualification probe cleanup failed")


def inspect(binary, fixture, output):
    config = json.loads((fixture / "cloudforge.yaml").read_text())  # JSON is a YAML subset.
    assert config["schema_version"] == "v1alpha5"
    package = json.loads((fixture / "package.json").read_text())
    lock = json.loads((fixture / "package-lock.json").read_text())
    assert lock["packages"]["node_modules/pg"]["version"] == package["dependencies"]["pg"]
    assert "@sha256:" in (fixture / "Dockerfile").read_text()
    for name in ("server.js", "prepare.js", "network-probe.js"):
        subprocess.run(["node", "--check", str(fixture / name)], check=True, timeout=15)
    for name in ("analyze", "verify"):
        command = [binary, name, str(fixture), "--format", "json"]
        if name == "verify":
            command.append("--plan")
        first = subprocess.run(command, capture_output=True, timeout=30, check=True)
        second = subprocess.run(command, capture_output=True, timeout=30, check=True)
        (output / f"{name}.stdout.json").write_bytes(first.stdout)
        (output / f"{name}.stderr.txt").write_bytes(first.stderr)
        assert first.stdout == second.stdout, "inspection is not deterministic"
        if name == "verify":
            plan = json.loads(first.stdout)
            assert plan["status"] == "pass", plan
            capabilities = {c["name"]: c for c in plan["capabilities"]}
            for capability in ("dependency.postgresql", "dependency.redis", "dependency.clamav", "application-preparation", "network-isolation"):
                assert capabilities[capability]["disposition"] == "supported", capabilities
    return config


def run_case(binary, fixture, base_config, name, output, repository):
    output.mkdir()
    config = copy.deepcopy(base_config)
    if name == "preparation-failure":
        config["preparation"]["command"].append("--fail")
    elif name == "missing-key":
        del config["environment"]["APP_TEST_KEY"]
    elif name == "missing-schema":
        del config["preparation"]
    config_path = output / "runtime.yaml"
    write_json(config_path, config)
    command = [binary, "verify", str(fixture), "--config", str(config_path), "--format", "json"]
    write_json(output / "command.json", command)
    with tempfile.TemporaryDirectory(prefix="cf-backend-qualification-") as temporary:
        private = Path(temporary)
        wrapper = private / "kubectl"
        wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable,os.environ['CF_BACKEND_HARNESS'],'__kubectl',*sys.argv[1:]])\n")
        wrapper.chmod(0o700)
        secret_path = private / "secret-canaries.json"
        environment = dict(os.environ, TMPDIR=temporary, PATH=temporary + os.pathsep + os.environ["PATH"],
                           CF_BACKEND_REAL_KUBECTL=shutil.which("kubectl"), CF_BACKEND_HARNESS=str(Path(__file__).resolve()),
                           CF_BACKEND_SECRET_CANARIES=str(secret_path), CF_BACKEND_CASE_OUTPUT=str(output),
                           CF_BACKEND_NETWORK_PROBES="true" if name == "healthy" else "false")
        with (output / "stdout.json").open("wb") as stdout, (output / "stderr.txt").open("wb") as stderr:
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=environment)
            try:
                code = process.wait(timeout=2400)
            except (subprocess.TimeoutExpired, KeyboardInterrupt):
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=720)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                raise
            finally:
                write_json(output / "exit.json", {"exit_code": process.returncode})
                cleanup = subprocess.run(["bash", str(repository / "scripts/pilot-cleanup-check.sh")], capture_output=True, timeout=60)
                (output / "cleanup.stdout.txt").write_bytes(cleanup.stdout)
                (output / "cleanup.stderr.txt").write_bytes(cleanup.stderr)
                write_json(output / "cleanup.json", {"exit_code": cleanup.returncode})
        assert cleanup.returncode == 0, "owned runtime resources survived cleanup"
        assert not list(private.glob("cloudforge-verify-*")), "private CloudForge workspace leaked"
        raw = (output / "stdout.json").read_bytes() + (output / "stderr.txt").read_bytes()
        canaries = json.loads(secret_path.read_text()) if secret_path.exists() else []
        assert canaries, "generated secret omission was not observable"
        assert not any(value.encode() in raw for value in canaries), "generated test material leaked in CLI output"
        write_json(output / "secret-omission.json", {"actual_run_values_checked": True, "values_retained": False, "passed": True})

    report_path = output / "stdout.json"
    report = json.loads(report_path.read_text())
    assert report["schema_version"] == "v1alpha7"
    assert report["producer"]["commit"] not in ("", "unknown")
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    for provider in ("postgresql", "redis", "clamav"):
        assert evidence["dependency." + provider]["status"] == "pass", evidence
    assert evidence["network-isolation"]["status"] == "pass", evidence
    if name == "preparation-failure":
        assert code == 1 and report["status"] == "blocked", report["status"]
        assert evidence["application-preparation"]["status"] == "fail"
        assert not evidence["deployment-readiness"].get("execution", {}).get("executed", False)
    elif name in ("missing-key", "missing-schema"):
        assert code == 1 and report["status"] == "fail", report["status"]
        assert evidence["deployment-readiness"]["status"] == "fail", evidence
    else:
        assert code in (0, 1) and report["status"] in ("pass", "warn", "fail"), report["status"]
        for experiment in ("container-build", "application-preparation", "deployment-readiness", "semantic-readiness", "readiness-gating", "inflight-shutdown", "load-profile"):
            assert evidence[experiment]["status"] == "pass", evidence[experiment]
        for experiment in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
            item = evidence[experiment]
            assert item["status"] in ("pass", "fail") and item["execution"]["executed"], item
            assert item["recovery"]["status"] == "pass", item
        assert json.loads((output / "network-enforcement.json").read_text())["cleanup"]
    if report["status"] == "fail":
        assert code == 1, "application failure was erased by successful restoration"
    canonical = subprocess.check_output([binary, "report", str(report_path), "--format", "json"], timeout=30)
    assert canonical == subprocess.check_output([binary, "report", str(report_path), "--format", "json"], timeout=30)
    (output / "canonical.json").write_bytes(canonical)
    (output / "report.md").write_bytes(subprocess.check_output([binary, "report", str(report_path), "--format", "markdown"], timeout=30))
    print(f"{name}: CloudForge exit={code}, status={report['status']}; evidence and cleanup retained", flush=True)
    return {"case": name, "exit_code": code, "status": report["status"], "cleanup": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--run", action="store_true", help="execute on a disposable GitHub-hosted runner")
    parser.add_argument("--case", action="append", choices=("healthy", "preparation-failure", "missing-key", "missing-schema"))
    args = parser.parse_args()
    repository = Path(__file__).resolve().parent.parent
    fixture = repository / "testdata/backend-http"
    binary, output = str(args.binary.resolve()), args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    snapshot = lambda: {str(p.relative_to(fixture)): hashlib.sha256(p.read_bytes()).hexdigest()
                        for p in sorted(fixture.rglob("*")) if p.is_file()}
    before = snapshot()
    config = inspect(binary, fixture, output)
    results = []
    if args.run:
        if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
            parser.error("--run requires a disposable GitHub-hosted runner")
        for case in args.case or ("healthy", "preparation-failure", "missing-key", "missing-schema"):
            results.append(run_case(binary, fixture, config, case, output / case, repository))
    assert before == snapshot(), "fixture source changed during qualification"
    write_json(output / "checks.json", {"source_unchanged": True, "static_inspection": True, "runtime_executed": args.run, "cases": results})


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "__kubectl":
        sys.exit(kubectl_wrapper(sys.argv[2:]))
    main()
