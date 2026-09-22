#!/usr/bin/env python3
"""Observe real HTTP FAIL, then inject only its identified restoration apply error."""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile

from qualification_command import run_observed

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("restoration_public_guards", ROOT / "scripts/pilot-backend-cancellation.py")
guards = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guards)
CASE = "restoration-failure"
SOURCE = ROOT / "testdata/broken-shutdown"
CONTEXT = re.compile(r"k3d-cloudforge-([a-f0-9]{20})")
LATER = ("graceful-shutdown", "pod-recovery", "rolling-deployment", "load-profile")


def write(path, value):
    with path.open("x") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")


def read(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 8192:
        raise ValueError("unusable restoration observer state")
    return json.loads(path.read_text())


def validate_fixture(root):
    if root.is_symlink() or root.resolve() != root or not root.name.startswith("cloudforge-restoration-"):
        raise ValueError("restoration observer requires its private fixture root")
    fixture = root / "fixture"
    for directory in (SOURCE, fixture):
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError("restoration observer requires the bundled public fixture")
        for item in directory.rglob("*"):
            if not (stat.S_ISREG(item.lstat().st_mode) or stat.S_ISDIR(item.lstat().st_mode)):
                raise ValueError("restoration fixture contains nonregular source")
    expected = guards.hashes(SOURCE)
    if guards.hashes(fixture) != expected:
        raise ValueError("copied restoration fixture differs from bundled public source")
    return expected


def apply_identity(root, arguments):
    if len(arguments) != 5 or arguments[0] != "--context" or arguments[2:4] != ["apply", "--filename"]:
        return None
    match = CONTEXT.fullmatch(arguments[1])
    if not match:
        raise ValueError("restoration apply has an unrelated context")
    manifest = Path(arguments[4])
    if (manifest.name != "workload.yaml" or manifest.parent.parent != root
            or not manifest.parent.name.startswith("cloudforge-verify-")
            or manifest.resolve() != manifest or manifest.is_symlink()
            or not manifest.is_file() or manifest.stat().st_size > 1024 * 1024
            or stat.S_IMODE(manifest.stat().st_mode) != 0o600):
        raise ValueError("restoration apply requires the private generated workload file")
    return {"context": arguments[1], "run_id": match.group(1), "manifest": str(manifest),
            "manifest_sha256": hashlib.sha256(manifest.read_bytes()).hexdigest()}


def slow_identity(arguments):
    if len(arguments) != 6 or arguments[0] != "--context" or arguments[2:5] != ["--request-timeout=15s", "get", "--raw"]:
        return None
    match = CONTEXT.fullmatch(arguments[1])
    if not match:
        return None
    run_id = match.group(1)
    route = re.fullmatch(r"/api/v1/namespaces/cloudforge/pods/(cf-broken-shutdown-api-" + run_id
                         + r"-[a-z0-9-]+):8080/proxy/_test/slow\?id=" + run_id + r"-shutdown", arguments[5])
    if not route:
        return None
    return {"context": arguments[1], "run_id": run_id, "pod": route.group(1), "request_id": run_id + "-shutdown"}


def is_target_delete(arguments, started):
    return arguments == ["--context", started["context"], "--namespace", "cloudforge", "delete", "pod", started["pod"], "--wait=false"]


def injection_proof(root, identity):
    baseline = read(root / "baseline.json")
    if baseline != identity:
        raise ValueError("restoration workload identity changed")
    paths = [root / name for name in ("slow-started.json", "delete-completed.json", "slow-completed.json")]
    if not all(path.exists() for path in paths):
        return None
    started, deleted, completed = (read(path) for path in paths)
    if (started.get("context") != identity["context"] or started.get("run_id") != identity["run_id"]
            or deleted.get("target") != started or completed.get("target") != started
            or type(deleted.get("exit_code")) is not int or deleted["exit_code"] != 0
            or type(completed.get("exit_code")) is not int or not 0 < completed["exit_code"] < 128):
        return None
    return {"case": CASE, "injected_tool": "kubectl", "injected_operation": "restore_same_owned_workload_apply",
            "context": identity["context"], "manifest_name": "workload.yaml", "manifest_sha256": identity["manifest_sha256"],
            "original_targeted_request": started, "original_delete_exit": 0,
            "original_slow_exit": completed["exit_code"], "injected_exit": 70,
            "scope": "unchanged_bundled_broken_shutdown_fixture_only", "observed_at": datetime.now(timezone.utc).isoformat(),
            "boundary": "Real request and deletion outputs are forwarded unchanged; only the later restoration apply is refused. Native FAIL must still be verified in the final report."}


def observed_command(real, arguments, root, name, timeout):
    return guards.tee_command([real, *arguments], root / "commands", CASE, sys.stdout.buffer, sys.stderr.buffer,
                              scope="unchanged_bundled_broken_shutdown_fixture_only", tool="kubectl", name=name,
                              native_timeout_seconds=timeout)


def tool_wrapper(arguments):
    guards.require_runner()
    root = Path(os.environ["CF_RESTORATION_ROOT"])
    validate_fixture(root)
    real = os.environ["CF_RESTORATION_REAL_KUBECTL"]
    identity = apply_identity(root, arguments)
    if identity is not None:
        if not (root / "baseline.json").exists():
            code = observed_command(real, arguments, root, "initial-apply", 60)
            if code == 0:
                write(root / "baseline.json", identity)
            return code
        proof = injection_proof(root, identity)
        if proof is not None:
            write(root / "injection-observed.json", proof)
            print("intentional qualification fault: the identified restoration apply is unavailable", file=sys.stderr)
            return 70
    started = slow_identity(arguments)
    if started is not None:
        baseline = read(root / "baseline.json")
        if baseline["context"] != started["context"]:
            raise ValueError("targeted request context differs from the initial workload")
        write(root / "slow-started.json", started)
        code = observed_command(real, arguments, root, "original-slow-request", 20)
        write(root / "slow-completed.json", {"target": started, "exit_code": code})
        return code
    if (root / "slow-started.json").exists():
        started = read(root / "slow-started.json")
        if is_target_delete(arguments, started):
            code = observed_command(real, arguments, root, "original-target-delete", 15)
            write(root / "delete-completed.json", {"target": started, "exit_code": code})
            return code
    os.execv(real, [real, *arguments])


def qualify(report, exit_code, producer):
    assert report["status"] == "error" and exit_code == 2, "failed restoration must retain overall ERROR/exit 2"
    assert report["producer"] == producer, "native producer must match the executed binary"
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    assert len(evidence) == len(report["evidence"]), "duplicate native evidence identities"
    required = {"container-build", "container-scan", "deployment-readiness", "readiness-gating", "inflight-shutdown", "horizontal-autoscaling", "environment-cleanup", *LATER}
    assert required.issubset(evidence), "required native evidence is missing"
    for name in ("container-build", "deployment-readiness"):
        assert evidence[name]["status"] == "pass", "earlier successful evidence was lost: " + name
    assert evidence["container-scan"]["status"] in ("pass", "warn")
    assert evidence["readiness-gating"]["status"] == "skipped" and evidence["readiness-gating"].get("execution", {}).get("executed") is False, "the unchanged one-replica fixture skips readiness gating"
    failed = evidence["inflight-shutdown"]
    assert failed["status"] == "fail", "the original real application FAIL must remain"
    assert failed["execution"] == {"executed": True, "mutation_attempted": True}, "targeted shutdown must actually mutate"
    measurements = {item["name"]: item["value"] for item in failed.get("measurements", [])}
    assert measurements.get("active_before_delete") == "true", "the target must acknowledge the active request"
    assert measurements.get("target_pod") and measurements.get("request_id"), "retain original target/request identity"
    recovery = failed["recovery"]
    assert recovery["status"] == "error" and recovery["strategy"] == "restore_original_deployment_and_validate", recovery
    assert recovery["summary"] == "The intended workload manifest could not be restored.", recovery
    assert recovery.get("checks") == [], "no restoration validation may be invented after failed apply"
    for name in LATER:
        assert evidence[name]["status"] == "blocked", "dependent work must be BLOCKED: " + name
        assert evidence[name].get("execution", {}).get("executed") is False, "dependent work executed: " + name
        assert not evidence[name].get("execution", {}).get("mutation_attempted", False)
    assert evidence["horizontal-autoscaling"]["status"] == "skipped"
    assert evidence["horizontal-autoscaling"].get("execution", {}).get("executed") is False
    assert any(item["id"] == "runtime.inflight-shutdown" and item["status"] == "fail" for item in report["findings"]), "original FAIL finding was erased"
    assert any(item["code"] == "inflight_shutdown_failed" and item["status"] == "fail" for item in report["diagnostics"]), "original FAIL diagnostic was erased"
    cleanup = evidence["environment-cleanup"]
    assert cleanup["status"] == "pass" and cleanup.get("execution", {}).get("executed") is True, "native final cleanup must run and PASS"


def cleanup_observation(output, baseline):
    command = [sys.executable, str(ROOT / "scripts/pilot-cleanup-check.py"), "--baseline", str(baseline), "--output", str(output / "independent-cleanup.json")]
    with (output / "independent-cleanup.stdout.txt").open("x") as stdout, (output / "independent-cleanup.stderr.txt").open("x") as stderr:
        result = subprocess.run(command, stdout=stdout, stderr=stderr, timeout=60, check=False)
    write(output / "independent-cleanup.exit.json", {"exit_code": result.returncode})
    return result.returncode


def main():
    if sys.argv[1:2] == ["__tool"]:
        code = tool_wrapper(sys.argv[2:])
        if code < 0:
            received = -code
            if received not in (signal.SIGKILL, signal.SIGSTOP):
                signal.signal(received, signal.SIG_DFL)
            os.kill(os.getpid(), received)
        return code
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    guards.require_runner()
    args.binary = args.binary.resolve()
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=False)
    identity_result = guards.capture([str(args.binary), "version", "--format", "json"], 15, 8192)
    (args.output / "binary-version.json").write_bytes(identity_result.stdout)
    if identity_result.returncode:
        raise ValueError("executed binary identity unavailable")
    identity = json.loads(identity_result.stdout)
    producer = {"version": identity["version"], "commit": identity["commit"]}
    if not re.fullmatch(r"[a-f0-9]{40}", producer["commit"]):
        raise ValueError("executed binary needs a complete source identity")
    baseline = args.output / "cleanup-baseline.json"
    subprocess.run([sys.executable, str(ROOT / "scripts/pilot-cleanup-check.py"), "--capture-baseline", str(baseline)], check=True, timeout=15)
    with tempfile.TemporaryDirectory(prefix="cloudforge-restoration-") as temporary:
        root = Path(temporary)
        shutil.copytree(SOURCE, root / "fixture")
        hashes = validate_fixture(root)
        write(args.output / "fault-policy.json", {"case": CASE, "fixture": "testdata/broken-shutdown", "source_hashes": hashes,
              "source_changes": [], "retries": 0, "producer": producer,
              "proof_type": "executed-binary runtime with a declared restoration fault; caller retains archive installation proof",
              "fault": "refuse only the original private workload apply after matching real targeted deletion and failed request"})
        bin_dir = root / "bin"
        bin_dir.mkdir()
        real = shutil.which("kubectl")
        if not real:
            raise ValueError("kubectl unavailable before restoration injection")
        wrapper = bin_dir / "kubectl"
        wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable," + repr(str(Path(__file__).resolve())) + ", '__tool', *sys.argv[1:]])\n")
        wrapper.chmod(0o700)
        environment = dict(os.environ, CF_RESTORATION_ROOT=str(root), CF_RESTORATION_REAL_KUBECTL=real,
                           TMPDIR=str(root), PATH=str(bin_dir) + os.pathsep + os.environ["PATH"])
        try:
            result = run_observed([str(args.binary), "verify", str(root / "fixture"), "--format", "json"], args.output / "verification.json", timeout=1100, env=environment)
        finally:
            for name in ("injection-observed.json", "slow-started.json", "slow-completed.json", "delete-completed.json"):
                if (root / name).exists():
                    shutil.copyfile(root / name, args.output / name)
            if (root / "commands").exists():
                shutil.copytree(root / "commands", args.output / "commands")
            remaining = sorted(item.name for item in root.glob("cloudforge-verify-*"))
            write(args.output / "nested-workspace-observation.json", {"status": "ERROR" if remaining else "PASS", "remaining": remaining})
            cleanup_code = cleanup_observation(args.output, baseline)
        assert guards.hashes(root / "fixture") == hashes, "public source changed during qualification"
        assert not remaining, "private runtime workspace remained before harness disposal"
        assert cleanup_code == 0, "independent resource absence was not established"
        assert (args.output / "injection-observed.json").is_file(), "identified restoration fault was not reached"
        qualify(json.loads(result.stdout), result.returncode, producer)
    print("restoration-failure: real application FAIL preserved, injected recovery ERROR, later work BLOCKED, cleanup PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
