#!/usr/bin/env python3
"""Interrupt bundled public fixtures and retain bounded import/cleanup evidence.

Public fixture validation and stream teeing add small wrapper overhead inside
CloudForge's original command deadlines; no retry, new runtime API call, traffic
change or command timeout extension is performed by the command observer.
"""
from qualification_record import observation_started, observation_finished
import argparse
from contextlib import contextmanager
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
import time

REPOSITORY = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("bounded_command_evidence", REPOSITORY / "scripts/pilot-backend-cancellation.py")
helpers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helpers)
STAGES = ("build", "cluster", "readiness", "lifecycle", "load", "redis")
CLEANUP_SECONDS = 720
# Public IDs are currently 20 hex characters; archived fixtures use 8 or 32.
RUN_NAME = re.compile(r"cloudforge-[a-f0-9]{8,32}")
INJECTED_EXIT = 70
INJECTED_ERROR = b"CloudForge public qualification intentionally withheld the first builder removal.\n"
PREFIX = "CLOUDFORGE_PILOT_"


def fixture_copy(target, stage, monorepo, probe_pacing=False):
    if probe_pacing and (stage != "readiness" or monorepo):
        raise helpers.QualificationError("probe_pacing_requires_public_readiness_case")
    source = REPOSITORY / "testdata" / ("monorepo" if monorepo else "healthy-node-redis" if stage == "redis" else "healthy-node")
    shutil.copytree(source, target)
    if stage == "build":
        dockerfile = target / ("apps/http/Containerfile.release" if monorepo else "Dockerfile")
        dockerfile.write_text(dockerfile.read_text().replace("WORKDIR /app", "RUN sleep 60\nWORKDIR /app"))
    if stage == "readiness":
        server = target / "server.js"
        server.write_text(server.read_text().replace("let ready = true", "let ready = false"))
    if stage == "lifecycle":
        server = target / "server.js"
        # Public cancellation fixture: keep the real pod deletion in progress.
        server.write_text(server.read_text().replace("process.exit(0)), 2000)", "process.exit(0)), 20000)"))
    config = target / "cloudforge.yaml"
    config.write_text(config.read_text().split("experiments:")[0])
    if probe_pacing:
        config.write_text(config.read_text().replace("schema_version: v1alpha1", "schema_version: v1alpha7", 1)
                          + "\nprobes:\n  interval: 2s\n")


def validate_public_fixture(fixture, stage, monorepo, probe_pacing=False):
    helpers.require_runner()
    if stage not in STAGES or (monorepo and stage != "build"):
        raise helpers.QualificationError("cleanup_capture_unknown_fixture_mode")
    if (fixture.is_symlink() or fixture.name != "app" or not fixture.parent.name.startswith("cloudforge-cancel-")
            or fixture.resolve() != fixture):
        raise helpers.QualificationError("cleanup_capture_requires_bundled_public_fixture")
    for path in fixture.rglob("*"):
        mode = path.lstat().st_mode
        if not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
            raise helpers.QualificationError("cleanup_capture_nonregular_source")
    with tempfile.TemporaryDirectory(prefix="cf-public-cleanup-proof-") as temporary:
        expected = Path(temporary) / "app"
        fixture_copy(expected, stage, monorepo, probe_pacing)
        if helpers.hashes(expected) != helpers.hashes(fixture):
            raise helpers.QualificationError("cleanup_capture_public_fixture_changed")


def created_run(tool, arguments):
    if tool == "docker" and len(arguments) == 8 and arguments[:3] == ["buildx", "create", "--name"]:
        name = arguments[3]
        base = "memory=2g,cpu-period=100000,cpu-quota=200000"
        if (RUN_NAME.fullmatch(name) and arguments[4:7] == ["--driver", "docker-container", "--driver-opt"]
                and (arguments[7] in (base, base + ",env.CLOUDFORGE_RUN_ID=" + name)
                     or re.fullmatch(re.escape(base + ",env.CLOUDFORGE_RUN_ID=" + name) + r",env.CLOUDFORGE_OWNER_ID=[a-f0-9]{32}", arguments[7]))):
            return name
    if tool == "k3d" and len(arguments) >= 4 and arguments[:2] == ["cluster", "create"] and RUN_NAME.fullmatch(arguments[2]):
        # Only register the name; no create output, runtime flags or credentials
        # are retained by the cleanup observer.
        return arguments[2]
    return None


def cleanup_operation(tool, arguments, owner):
    if not isinstance(owner, str) or not RUN_NAME.fullmatch(owner):
        return None
    if tool == "docker" and arguments == ["buildx", "rm", "--force", owner]:
        return "builder-remove", 60
    if tool == "k3d" and arguments == ["cluster", "delete", owner]:
        return "cluster-delete", 120
    return None


def validate_public_import(arguments, owner, stage, monorepo):
    """Allow only the registered run's exact bundled image A/B direct import."""
    if (stage not in STAGES or (monorepo and stage != "build")
            or len(arguments) != 7 or arguments[:2] != ["image", "import"]
            or arguments[3] != "--cluster" or arguments[5:] != ["--mode", "direct"]):
        raise helpers.QualificationError("import_capture_requires_known_public_import")
    if not isinstance(owner, str) or not RUN_NAME.fullmatch(owner) or arguments[4] != owner:
        raise helpers.QualificationError("import_capture_requires_registered_owner")
    name = "monorepo-http" if monorepo else "healthy-node-redis" if stage == "redis" else "healthy-node-api"
    image = "cloudforge/" + name + ":" + owner.removeprefix("cloudforge-")
    if arguments[2] not in (image + "-a", image + "-b"):
        raise helpers.QualificationError("import_capture_refused_unrelated_image")


def finish_signal(code):
    if code < 0:
        received = -code
        if received not in (signal.SIGKILL, signal.SIGSTOP):
            signal.signal(received, signal.SIG_DFL)
        os.kill(os.getpid(), received)
    return code


def retain_cleanup(output, stage):
    """Preserve cleanup observations even when qualification itself failed."""
    try:
        result = subprocess.run(["bash", str(REPOSITORY / "scripts/pilot-cleanup-check.sh")], capture_output=True, timeout=30)
        (output / f"cancel-{stage}.cleanup.stdout.txt").write_bytes(result.stdout)
        (output / f"cancel-{stage}.cleanup.stderr.txt").write_bytes(result.stderr)
        helpers.write_json(output / f"cancel-{stage}.cleanup.exit.json", {"exit_code": result.returncode, "completion_observed": True})
        return result.returncode == 0
    except (OSError, subprocess.SubprocessError):
        helpers.write_json(output / f"cancel-{stage}.cleanup.exit.json", {"exit_code": None, "completion_observed": False})
        return False


def qualify_cancellation(report, stage):
    """Require cancellation at the selected public phase without later work."""
    assert stage in STAGES, "Unknown cancellation stage"
    assert report["status"] == "error", "Unexpected native status"
    assert any(item["code"] == "verification_canceled" for item in report["diagnostics"]), "Missing cancellation classification"
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    target = {"build": "container-build", "redis": "dependency.redis", "readiness": "deployment-readiness",
              "lifecycle": "graceful-shutdown", "load": "load-profile"}.get(stage)
    allowed = {"environment-cleanup"}
    if target:
        allowed.add(target)
        assert evidence[target]["execution"]["executed"] is True, "Started experiment was presented as never executed"
        assert evidence[target]["status"] == "error", "Started experiment lost ERROR"
    if stage != "build":
        for name, statuses in (("container-build", ("pass",)), ("container-scan", ("pass", "warn"))):
            allowed.add(name)
            assert evidence[name]["execution"]["executed"] is True and evidence[name]["status"] in statuses, "Earlier build or scan evidence was erased"
    if stage in ("lifecycle", "load"):
        allowed.add("deployment-readiness")
        assert evidence["deployment-readiness"]["execution"]["executed"] is True and evidence["deployment-readiness"]["status"] == "pass", "Earlier readiness evidence was erased"
    if stage == "lifecycle":
        assert evidence[target]["execution"]["mutation_attempted"] is True, "Lifecycle mutation was not attempted"
    if stage == "load":
        # HPA observation and mutation run alongside load, not after it.
        allowed.add("horizontal-autoscaling")
        for name in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
            allowed.add(name)
            item = evidence[name]
            assert item["execution"]["executed"] is True and item["status"] in ("pass", "fail"), "Earlier lifecycle evidence was erased"
            recovery = item.get("recovery", {})
            assert recovery.get("status") == "pass" and recovery.get("checks") and all(check["status"] == "pass" for check in recovery["checks"]), "Earlier restoration was not established"
    if stage == "redis":
        assert any(item.get("name") == "redis" and item["status"] == "error" for item in report["dependencies"]), "Redis cancellation evidence was erased"
    expected = {"deployment-readiness", "semantic-readiness", "readiness-gating", "inflight-shutdown", "graceful-shutdown",
                "pod-recovery", "rolling-deployment", "load-profile", "horizontal-autoscaling", "dependency-loss"}
    for name in expected - allowed:
        assert evidence.get(name, {}).get("execution", {}).get("executed") is False, "Missing evidence that later work remained unexecuted: " + name
    for name, item in evidence.items():
        if name not in allowed:
            assert item.get("execution", {}).get("executed") is False, "New work started after cancellation: " + name


def tool_wrapper(tool, arguments):
    helpers.require_runner()
    if tool not in ("docker", "k3d", "kubectl", "k6"):
        raise helpers.QualificationError("cleanup_capture_unknown_tool")
    real = os.environ["CLOUDFORGE_REAL_" + tool.upper()]
    stage = os.environ[PREFIX + "STAGE"]
    fixture = Path(os.environ[PREFIX + "FIXTURE"])
    monorepo = os.environ.get(PREFIX + "MONOREPO") == "true"
    pacing_value = os.environ.get(PREFIX + "PROBE_PACING", "false")
    if pacing_value not in ("false", "true"):
        raise helpers.QualificationError("probe_pacing_requires_fixed_public_policy")
    validate_public_fixture(fixture, stage, monorepo, pacing_value == "true")
    if os.environ.get(PREFIX + "FORCE_BUILDER_FAILURE") == "true" and (stage != "redis" or monorepo):
        raise helpers.QualificationError("builder_fault_requires_public_redis_case")
    owner_path = Path(os.environ[PREFIX + "OWNER"])
    owner = json.loads(owner_path.read_text())["run_name"] if owner_path.exists() else None
    created = created_run(tool, arguments)
    if created:
        if owner not in (None, created):
            raise helpers.QualificationError("cleanup_capture_multiple_run_names")
        owner = created
        helpers.write_json(owner_path, {"run_name": owner})
    if tool == "k3d" and arguments[:2] == ["image", "import"]:
        validate_public_import(arguments, owner, stage, monorepo)
        return finish_signal(helpers.tee_command([real, *arguments], Path(os.environ[PREFIX + "IMPORT_OUTPUT"]),
                stage, sys.stdout.buffer, sys.stderr.buffer, scope="bundled_public_cancellation_fixture_only",
                tool=tool, name="k3d-import", native_timeout_seconds=180))
    operation = cleanup_operation(tool, arguments, owner)
    if operation:
        output = Path(os.environ[PREFIX + "CLEANUP_OUTPUT"])
        if os.environ.get(PREFIX + "FORCE_BUILDER_FAILURE") == "true" and operation[0] == "builder-remove":
            if stage != "redis" or monorepo:
                raise helpers.QualificationError("builder_fault_requires_public_redis_case")
            marker = Path(os.environ[PREFIX + "INJECTION"])
            if not marker.exists():
                # A declared separate qualification case, never an ordinary run.
                # This records a withheld command, not an observed Docker error.
                record = {"scope": "bundled_public_redis_fixture_only", "injected": True,
                          "actual_command_executed": False, "tool": tool, "arguments": arguments,
                          "exit_code": INJECTED_EXIT, "retry_performed": False,
                          "reason": "intentionally_withheld_first_builder_removal"}
                helpers.write_json(marker, record)
                output.mkdir(parents=True, exist_ok=True)
                helpers.write_json(output / "injected-builder-removal.json", record)
                (output / "injected-builder-removal.stderr.txt").write_bytes(INJECTED_ERROR)
                sys.stderr.buffer.write(INJECTED_ERROR)
                sys.stderr.buffer.flush()
                return INJECTED_EXIT
        return finish_signal(helpers.tee_command([real, *arguments], output, stage, sys.stdout.buffer, sys.stderr.buffer,
                scope="bundled_public_cancellation_fixture_only", tool=tool, name=operation[0], native_timeout_seconds=operation[1]))
    if stage == "redis" and tool == "kubectl" and "apply" in arguments and arguments[-1].endswith("dependency-redis.yaml"):
        path = Path(arguments[-1])
        if path.is_symlink() or path.resolve().parent.parent != fixture.parent:
            raise helpers.QualificationError("redis_hold_requires_owned_temporary_manifest")
        path.write_text(re.sub(r"(?m)^(\s+)periodSeconds: 1$", r"\1initialDelaySeconds: 60\n\1periodSeconds: 1", path.read_text()))
    selected = ((stage == "redis" and tool == "kubectl" and "rollout" in arguments and "deployment/cf-dependency-redis" in arguments)
                or (stage == "build" and tool == "docker" and "build" in arguments)
                or (stage == "cluster" and tool == "k3d" and "create" in arguments)
                or (stage == "readiness" and tool == "kubectl" and "rollout" in arguments and "status" in arguments)
                or (stage == "lifecycle" and tool == "kubectl" and "delete" in arguments and "pod" in arguments and "--wait=true" in arguments)
                or (stage == "load" and tool == "k6" and "run" in arguments))
    if not selected:
        os.execv(real, [real, *arguments])
    process = subprocess.Popen([real, *arguments])
    helpers.write_json(Path(os.environ[PREFIX + "MARKER"]), {"tool": tool, "pid": process.pid})
    return finish_signal(process.wait())


@contextmanager
def existing_state():
    original_kubeconfig = Path.home() / ".kube" / "config"
    original_bytes = original_kubeconfig.read_bytes() if original_kubeconfig.exists() else None
    """Use recognizable pre-existing state to prove cleanup ownership boundaries."""
    name = "pilot-existing-" + str(os.getpid())
    created = []
    try:
        subprocess.run(["docker", "network", "create", name], check=True, capture_output=True, timeout=30)
        created.append("network")
        subprocess.run(["docker", "volume", "create", name], check=True, capture_output=True, timeout=30)
        created.append("volume")
        subprocess.run(["docker", "run", "--detach", "--name", name, "--network", name, "--volume", name + ":/data", "alpine:3.22", "sleep", "3600"], check=True, capture_output=True, timeout=120)
        created.append("container")
        original_kubeconfig.parent.mkdir(parents=True, exist_ok=True)
        original_kubeconfig.write_text("apiVersion: v1\nkind: Config\ncurrent-context: existing\nclusters:\n- name: existing\n  cluster:\n    server: https://127.0.0.1:1\ncontexts:\n- name: existing\n  context:\n    cluster: existing\n    user: existing\nusers:\n- name: existing\n  user: {}\n")
        original_kubeconfig.chmod(0o600)
        baseline = original_kubeconfig.read_bytes()
        identities = {kind: subprocess.check_output(["docker", kind, "inspect", "--format", "{{.Id}}" if kind != "volume" else "{{.Name}}", name], text=True).strip() for kind in created}
        yield name, baseline, identities
    finally:
        for kind in reversed(created):
            arguments = ["docker", kind, "rm"]
            if kind == "container":
                arguments.append("--force")
            subprocess.run([*arguments, name], capture_output=True, timeout=30)
        if original_bytes is None:
            original_kubeconfig.unlink(missing_ok=True)
        else:
            original_kubeconfig.write_bytes(original_bytes)


def run_stage(args, stage, sentinel_name, baseline_kubeconfig, sentinel_ids):
    with tempfile.TemporaryDirectory(prefix="cloudforge-cancel-") as temporary:
        root = Path(temporary).resolve()
        app = root / "app"
        probe_pacing = getattr(args, "probe_pacing", False)
        fixture_copy(app, stage, args.monorepo, probe_pacing)
        validate_public_fixture(app, stage, args.monorepo, probe_pacing)
        bin_dir = root / "bin"
        bin_dir.mkdir()
        environment = dict(os.environ, TMPDIR=str(root))
        for tool in ("docker", "k3d", "kubectl", "k6"):
            real = shutil.which(tool)
            if not real:
                raise helpers.QualificationError("runtime_tool_missing")
            environment["CLOUDFORGE_REAL_" + tool.upper()] = real
            file = bin_dir / tool
            file.write_text("#!/usr/bin/env python3\nimport os,pathlib,sys\nos.execv(sys.executable,[sys.executable,os.environ['CLOUDFORGE_PILOT_HARNESS'],'__tool',pathlib.Path(sys.argv[0]).name,*sys.argv[1:]])\n")
            file.chmod(0o700)
        marker = root / "started.json"
        environment.update(PATH=str(bin_dir) + os.pathsep + os.environ["PATH"], **{
            PREFIX + "HARNESS": str(Path(__file__).resolve()), PREFIX + "STAGE": stage,
            PREFIX + "FIXTURE": str(app), PREFIX + "MONOREPO": str(args.monorepo).lower(),
            PREFIX + "PROBE_PACING": str(probe_pacing).lower(),
            PREFIX + "MARKER": str(marker), PREFIX + "OWNER": str(root / "owner.json"),
            PREFIX + "IMPORT_OUTPUT": str(args.output / ("import-commands-" + stage)),
            PREFIX + "CLEANUP_OUTPUT": str(args.output / ("cleanup-commands-" + stage)),
            PREFIX + "FORCE_BUILDER_FAILURE": str(args.force_builder_cleanup_failure).lower(),
            PREFIX + "INJECTION": str(root / "injected-builder-failure.json")})
        report_path = args.output / f"cancel-{stage}.json"
        command = [args.binary, "verify", str(app), "--format", "json"]
        helpers.write_json(args.output / f"cancel-{stage}.command.json", {"argv": command,
            "force_builder_cleanup_failure": args.force_builder_cleanup_failure, "outer_cleanup_wait_seconds": CLEANUP_SECONDS,
            "probe_policy": {"interval": "2s"} if probe_pacing else None})
        process, interrupted_at, cleanup_passed = None, None, False
        try:
            with report_path.open("wb") as stdout, (args.output / f"cancel-{stage}.stderr").open("wb") as stderr:
                observation_started(report_path)
                process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=environment)
                deadline = time.monotonic() + 600
                while not marker.exists() and process.poll() is None and time.monotonic() < deadline:
                    time.sleep(0.1)
                if not marker.exists():
                    raise helpers.QualificationError("target_cancellation_phase_not_reached")
                time.sleep(2)
                if process.poll() is not None:
                    raise helpers.QualificationError("verification_ended_before_interruption")
                interrupted_at = time.monotonic()
                process.send_signal(signal.SIGINT)
                if process.wait(timeout=CLEANUP_SECONDS) != 2:
                    raise helpers.QualificationError("cancellation_not_classified_as_execution_error")
        finally:
            if process is not None and process.poll() is None:
                if interrupted_at is None:
                    interrupted_at = time.monotonic()
                    process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=max(0, CLEANUP_SECONDS - (time.monotonic() - interrupted_at)))
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
            if process is not None:
                observation_finished(report_path, process.returncode if process else None)
                helpers.write_json(args.output / f"cancel-{stage}.exit.json", {"exit_code": process.returncode})
            cleanup_passed = retain_cleanup(args.output, stage)
        report = json.loads(report_path.read_text())
        qualify_cancellation(report, stage)
        if probe_pacing:
            assert report["plan"]["probes"] == {"interval": "2s"}, "Configured pacing missing from plan"
            assert report["fingerprint"]["configuration"]["probes"] == {"interval": "2s"}, "Configured pacing missing from fingerprint"
        if args.force_builder_cleanup_failure:
            assert Path(environment[PREFIX + "INJECTION"]).exists(), "Declared builder fault was not exercised"
            assert any(item["code"] == "builder_cleanup_failed" for item in report["diagnostics"]), "Original injected cleanup failure disappeared"
            assert not any(item["code"] == "builder_remnant_cleanup_failed" for item in report["diagnostics"]), "Builder fallback failed"
        assert not list(root.glob("cloudforge-verify-*")), "Temporary kubeconfig/runtime directory leaked"
        assert cleanup_passed, "CloudForge-owned resources remain after cleanup or could not be observed"
        current_config = Path.home() / ".kube" / "config"
        current_bytes = current_config.read_bytes() if current_config.exists() else None
        assert current_bytes == baseline_kubeconfig, "User kubeconfig changed"
        for kind, expected in sentinel_ids.items():
            actual = subprocess.check_output(["docker", kind, "inspect", "--format", "{{.Id}}" if kind != "volume" else "{{.Name}}", sentinel_name], text=True).strip()
            assert actual == expected, f"Unrelated {kind} changed"
        running = subprocess.check_output(["docker", "container", "inspect", "--format", "{{.State.Running}}", sentinel_name], text=True).strip()
        assert running == "true", "Unrelated container was stopped"
        print(f"{stage}: interrupted real subprocess; owned resources removed; kubeconfig unchanged", flush=True)


def main():
    if sys.argv[1:2] == ["__tool"]:
        return tool_wrapper(sys.argv[2], sys.argv[3:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("output", type=Path)
    parser.add_argument("--stages", nargs="+", default=list(STAGES[:-1]), choices=STAGES)
    parser.add_argument("--monorepo", action="store_true", help="Exercise selected custom Dockerfile cancellation (build stage only)")
    parser.add_argument("--probe-pacing", action="store_true", help="Fixed public readiness-cancellation case with explicit two-second HTTP probe pacing")
    parser.add_argument("--force-builder-cleanup-failure", action="store_true", help="Separate public Redis cancellation case: withhold first builder removal, retain ERROR, require owned fallback cleanup")
    args = parser.parse_args()
    if args.monorepo and args.stages != ["build"]:
        parser.error("--monorepo requires --stages build")
    if args.force_builder_cleanup_failure and (args.stages != ["redis"] or args.monorepo):
        parser.error("--force-builder-cleanup-failure requires only --stages redis")
    if args.probe_pacing and (args.stages != ["readiness"] or args.monorepo or args.force_builder_cleanup_failure):
        parser.error("--probe-pacing requires only --stages readiness")
    helpers.require_runner()  # Before output directories, Docker or kubeconfig access.
    args.binary = str(Path(args.binary).resolve())
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    if any(args.output.iterdir()):
        parser.error("qualification output must be empty; retain the previous attempt")
    with existing_state() as (sentinel_name, baseline, identities):
        for stage in args.stages:
            run_stage(args, stage, sentinel_name, baseline, identities)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except helpers.QualificationError as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
