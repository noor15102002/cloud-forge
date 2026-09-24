#!/usr/bin/env python3
"""Prove exact-binary endpoint rejection before any daemon access on a disposable runner."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from qualification_command import run_observed

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("endpoint_guards", ROOT / "scripts/pilot-backend-cancellation.py")
guards = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guards)
CASES = ("stored-context", "environment-context", "environment-host", "default-context-host", "tls-selector")
CONTEXT = "qualification-unsupported-context"
REMOTE = "tcp://cloudforge-endpoint.invalid:2375"
FORMAT = "{{json .Endpoints.docker.Host}}"


def context_configuration(directory):
    """Create only synthetic Docker CLI metadata; never invoke context mutation commands."""
    metadata = directory / "contexts" / "meta" / hashlib.sha256(CONTEXT.encode()).hexdigest()
    metadata.mkdir(parents=True)
    (metadata / "meta.json").write_text(json.dumps({"Name": CONTEXT, "Metadata": {}, "Endpoints": {"docker": {"Host": REMOTE, "SkipTLSVerify": False}}}) + "\n")
    (directory / "config.json").write_text(json.dumps({"currentContext": CONTEXT}) + "\n")


def selection_environment(original, case, config, wrapper_directory):
    if case not in CASES:
        raise ValueError("unknown endpoint qualification case")
    env = {key: value for key, value in original.items() if not key.startswith("DOCKER_")}
    env.update(DOCKER_CONFIG=str(config), PATH=str(wrapper_directory) + os.pathsep + original["PATH"])
    if case == "environment-context":
        env.update(DOCKER_CONTEXT=CONTEXT, DOCKER_HOST="unix:///var/run/docker.sock")
    elif case == "environment-host":
        env["DOCKER_HOST"] = REMOTE
    elif case == "default-context-host":
        env.update(DOCKER_CONTEXT="default", DOCKER_HOST=REMOTE)
    elif case == "tls-selector":
        env.update(DOCKER_HOST="unix:///var/run/docker.sock", DOCKER_TLS_VERIFY="1")
    return env


def qualify(report, exit_code, trace, case):
    assert case in CASES
    assert exit_code == 1 and report["status"] == "blocked", "unsupported endpoint did not produce native BLOCKED/1"
    assert any(item["code"] == "docker_endpoint_unsupported" and item["status"] == "blocked" for item in report.get("diagnostics", [])), "native endpoint diagnostic missing"
    assert not any(item.get("execution", {}).get("executed") for item in report.get("evidence", [])), "application experiment ran past endpoint rejection"
    assert not any(item["kind"] != "context_metadata" for item in trace), "a daemon or other runtime command was attempted"
    expected_metadata_calls = 1 if case in ("stored-context", "environment-context") else 0
    assert len(trace) == expected_metadata_calls, "endpoint resolution used unexpected commands or retries"
    encoded = json.dumps(report)
    assert REMOTE not in encoded and CONTEXT not in encoded, "native report exposed connection details"


def tool_wrapper(root, tool, arguments):
    guards.require_runner()
    root = Path(root)
    if root.is_symlink() or not root.name.startswith("cloudforge-endpoint-"):
        raise ValueError("endpoint guard requires its private qualification workspace")
    config = json.loads((root / "guard.json").read_text())
    allowed = tool == "docker" and arguments in (["context", "inspect", "--format", FORMAT], ["context", "inspect", "--format", FORMAT, "--", CONTEXT])
    entry = {"kind": "context_metadata" if allowed else "forbidden_runtime_command"}
    with (root / "trace.jsonl").open("a") as stream:
        stream.write(json.dumps(entry, sort_keys=True) + "\n")
    if not allowed:
        print("Endpoint qualification blocked an unexpected runtime command.", file=sys.stderr)
        return 97
    # The only delegated Docker command reads synthetic local context metadata.
    # Every daemon command is refused before reaching the actual executable.
    result = subprocess.run([config["docker"], *arguments], check=False, timeout=10)
    return result.returncode


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "__tool":
        return tool_wrapper(sys.argv[2], sys.argv[3], sys.argv[4:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    guards.require_runner()
    real_docker = shutil.which("docker")
    if not real_docker:
        raise ValueError("Docker CLI required for real context metadata observations")
    args.output.mkdir(parents=True, exist_ok=False)
    source = ROOT / "testdata/healthy-node"
    source_hashes = guards.hashes(source)
    (args.output / "policy.json").write_text(json.dumps({"scope": "bundled_public_fixture_preflight_only", "cases": list(CASES), "source_hashes": source_hashes, "daemon_access": "denied_by_guard", "retries": 0}, indent=2, sort_keys=True) + "\n")
    try:
        with tempfile.TemporaryDirectory(prefix="cloudforge-endpoint-") as temporary:
            root = Path(temporary)
            (root / "guard.json").write_text(json.dumps({"docker": real_docker}) + "\n")
            config, bin_directory = root / "docker", root / "bin"
            context_configuration(config)
            bin_directory.mkdir()
            for tool in ("docker", "k3d", "kubectl", "trivy", "k6"):
                wrapper = bin_directory / tool
                wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable," + repr(str(Path(__file__).resolve())) + ", '__tool', " + repr(str(root)) + ", " + repr(tool) + ", *sys.argv[1:]])\n")
                wrapper.chmod(0o700)
            for case in CASES:
                (root / "trace.jsonl").write_text("")
                environment = selection_environment(os.environ, case, config, bin_directory)
                environment["TMPDIR"] = str(root)
                try:
                    result = run_observed([str(args.binary.resolve()), "verify", str(source), "--format", "json"], args.output / (case + ".json"), timeout=30, env=environment, own_session=True)
                finally:
                    shutil.copyfile(root / "trace.jsonl", args.output / (case + ".commands.jsonl"))
                trace = [json.loads(line) for line in (root / "trace.jsonl").read_text().splitlines()]
                qualify(json.loads(result.stdout), result.returncode, trace, case)
                assert guards.hashes(source) == source_hashes, "bundled source changed during read-only preflight"
                assert not list(root.glob("cloudforge-verify-*")), "unexpected runtime workspace retained"
    finally:
        # Independent daemon inventory is outside the deny wrapper and retains
        # the ordinary disposable-runner ownership/cleanup guard.
        subprocess.run(["bash", str(ROOT / "scripts/pilot-cleanup-check.sh")], check=True, timeout=60)
    print("docker-endpoint-policy: five native BLOCKED outcomes retained; no daemon operation was attempted by verification")
    return 0


if __name__ == "__main__":
    sys.exit(main())
