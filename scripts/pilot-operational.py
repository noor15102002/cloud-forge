#!/usr/bin/env python3
"""Exact-binary startup and operational outcomes in a copied public fixture."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from qualification_command import run_observed
from reliability_import_observer import helpers as source_guards, public_files

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("public_guards", ROOT / "scripts/pilot-backend-cancellation.py")
guards = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guards)
CASES = ("scanner-null", "scanner-empty", "scanner-unsupported", "scanner-truncated", "scanner-zero", "scanner-findings", "scanner-case-alias",
         "scanner-subject", "scanner-exit", "docker-unavailable", "disk-full", "registry-503", "node-observation", "startup-failure", "cleanup-inventory")
SCANNER_METADATA = "scanner-wrapper.json"
SCANNER_METADATA_LIMIT = 32 * 1024
RUNNER_FIELDS = ("GITHUB_ACTIONS", "RUNNER_ENVIRONMENT", "RUNNER_OS")


def scanner_source_hashes(fixture):
    try:
        return {name: hashlib.sha256(data).hexdigest() for name, data in public_files(fixture).items()}
    except source_guards.QualificationError as error:
        raise ValueError("public operational fixture could not be read safely") from error


def scanner_root(root):
    information = root.lstat()
    if (not root.is_absolute() or root.resolve() != root or not root.name.startswith("cloudforge-operational-")
            or not stat.S_ISDIR(information.st_mode) or information.st_uid != os.getuid()
            or stat.S_IMODE(information.st_mode) != 0o700):
        raise ValueError("scanner wrapper requires its private operational root")


def create_scanner_metadata(root, case, real_tools, environment):
    """Capture only confirmed guards and public-fixture inputs before ClearEnv."""
    guards.require_runner(environment)
    scanner_root(root)
    if case not in CASES:
        raise ValueError("unknown operational scanner case")
    value = {"schema": "public-operational-scanner-v1", "root": str(root), "case": case,
             "tools": {tool: str(Path(real_tools[tool]).resolve(strict=True)) for tool in ("trivy", "docker")},
             "runner": {name: environment[name] for name in RUNNER_FIELDS},
             "source_hashes": scanner_source_hashes(root / "app")}
    payload = (json.dumps(value, sort_keys=True) + "\n").encode()
    if len(payload) > SCANNER_METADATA_LIMIT:
        raise ValueError("scanner wrapper metadata exceeds its bound")
    path = root / SCANNER_METADATA
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(payload)
    return path


def scanner_metadata(path):
    """Read an owned regular file without consulting or restoring caller env."""
    path = Path(path)
    if path.name != SCANNER_METADATA or not path.is_absolute():
        raise ValueError("scanner wrapper requires its metadata path")
    scanner_root(path.parent)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        information = os.fstat(stream.fileno())
        if (not stat.S_ISREG(information.st_mode) or information.st_uid != os.getuid()
                or stat.S_IMODE(information.st_mode) != 0o600 or information.st_size > SCANNER_METADATA_LIMIT):
            raise ValueError("scanner wrapper metadata ownership or bound is invalid")
        payload = stream.read(SCANNER_METADATA_LIMIT + 1)
    if len(payload) > SCANNER_METADATA_LIMIT:
        raise ValueError("scanner wrapper metadata exceeds its bound")

    def unique_fields(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("scanner wrapper metadata has duplicate fields")
            result[key] = value
        return result

    value = json.loads(payload, object_pairs_hook=unique_fields)
    if (not isinstance(value, dict)
            or set(value) != {"schema", "root", "case", "tools", "runner", "source_hashes"}
            or value["schema"] != "public-operational-scanner-v1"
            or value["root"] != str(path.parent) or value["case"] not in CASES
            or not isinstance(value["runner"], dict) or set(value["runner"]) != set(RUNNER_FIELDS)
            or not isinstance(value["tools"], dict) or set(value["tools"]) != {"trivy", "docker"}):
        raise ValueError("scanner wrapper metadata does not match its contract")
    guards.require_runner(value["runner"])
    for location in value["tools"].values():
        if not isinstance(location, str) or len(location) > 4096:
            raise ValueError("scanner wrapper tool path is invalid")
        executable = Path(location)
        if (not executable.is_absolute() or executable.resolve(strict=True) != executable
                or not stat.S_ISREG(executable.lstat().st_mode) or not os.access(executable, os.X_OK)
                or executable.is_relative_to(path.parent / "bin")):
            raise ValueError("scanner wrapper requires the real executable")
    if scanner_source_hashes(path.parent / "app") != value["source_hashes"]:
        raise ValueError("public operational fixture changed")
    return value


def write_tool_wrapper(path, tool, metadata):
    mode = ["__trivy", str(metadata)] if tool == "trivy" else ["__tool", tool]
    path.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable," +
                    repr(str(Path(__file__).resolve())) + ", *" + repr(mode) + ", *sys.argv[1:]])\n")
    path.chmod(0o700)


def trivy_observation(case, image, image_id):
    if case == "scanner-null":
        return "null"
    if case == "scanner-empty":
        return "{}"
    if case == "scanner-unsupported":
        return '{"unrelated":"observation"}'
    if case == "scanner-truncated":
        return '{"SchemaVersion":2,"Results":['
    value = {"SchemaVersion": 2, "ArtifactName": image, "ArtifactType": "container_image", "Metadata": {"ImageID": image_id}, "Results": []}
    if case == "scanner-subject":
        value["Metadata"]["ImageID"] = "sha256:" + "f" * 64
    if case in ("scanner-findings", "scanner-case-alias"):
        value["Results"] = [{"Target": "public-fixture", "Class": "os-pkgs", "Type": "debian", "Vulnerabilities": [
            {"VulnerabilityID": "CVE-2026-0001", "PkgName": "qualification-package", "InstalledVersion": "1.0", "FixedVersion": "1.1", "Severity": "HIGH"}]}]
    if case == "scanner-case-alias":
        value["results"] = []  # Must not overwrite the canonical nonempty Results.
    return json.dumps(value)


def qualify(report, exit_code, case):
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    if case == "startup-failure":
        assert exit_code == 1 and report["status"] == "fail", report
        assert evidence["container-build"]["status"] == "pass", "Independent application build PASS was erased"
        startup = evidence["deployment-readiness"]
        assert startup["status"] == "fail", startup
        measurements = {item["name"]: item["value"] for item in startup.get("measurements", [])}
        assert measurements.get("ready_pods") == "0" and measurements.get("total_pods") == "2", measurements
        diagnostic = next((item for item in report["diagnostics"] if item["code"] == "readiness_failed"), None)
        assert diagnostic is not None and diagnostic["status"] == "fail", report["diagnostics"]
        assert "Ready replicas: 0/2" in diagnostic["message"] and "Root cause: not established." in diagnostic["message"], diagnostic
        assert not any(word in diagnostic["message"] for word in ("OOMKilled", "out_of_memory", "missing configuration", "SIGTERM")), diagnostic
        assert not any(item["code"].startswith("runtime_environment_") for item in report["diagnostics"]), report["diagnostics"]
        assert not any(item["experiment_id"] in ("graceful-shutdown", "pod-recovery", "rolling-deployment", "load-profile")
                       and item.get("execution", {}).get("executed") for item in report["evidence"]), "Lifecycle work started after unmet startup readiness"
    elif case in ("scanner-zero", "scanner-findings"):
        expected = "pass" if case == "scanner-zero" else "warn"
        assert evidence["container-scan"]["status"] == expected, evidence["container-scan"]
        assert exit_code in (0, 1) and report["status"] != "error", report.get("diagnostics")
    else:
        assert exit_code == 2 and report["status"] == "error", report
        target = "container-scan" if case.startswith("scanner-") else "container-build" if case in ("disk-full", "registry-503") else "deployment-readiness" if case == "node-observation" else None
        if target:
            assert evidence[target]["status"] == "error", evidence[target]
        if case.startswith("scanner-"):
            assert not any(measurement["name"] == "vulnerabilities" for measurement in evidence["container-scan"].get("measurements", [])), evidence["container-scan"]
        if case == "cleanup-inventory":
            assert evidence["rolling-deployment"]["status"] == "fail", "Original rollout FAIL was not preserved"
            assert any(item["code"] == "cluster_remnant_cleanup_failed" for item in report["diagnostics"]), report["diagnostics"]
    if case.startswith("scanner-"):
        assert evidence["container-build"]["status"] == "pass", "Independent application build PASS was erased"
    if case != "docker-unavailable":
        expected_cleanup = "error" if case == "cleanup-inventory" else "pass"
        assert evidence["environment-cleanup"]["status"] == expected_cleanup, evidence["environment-cleanup"]
    assert report.get("diagnostics") or case in ("scanner-zero", "scanner-findings")


def node_health_observation(payload):
    """Retain condition codes only from the actual post-startup node observation."""
    if len(payload) > 256 * 1024:
        raise ValueError("node health observation exceeded the retained bound")
    nodes = json.loads(payload)["items"]
    if not isinstance(nodes, list) or not nodes:
        raise ValueError("node health observation did not contain nodes")
    retained = []
    fields = ("Ready", "DiskPressure", "MemoryPressure", "PIDPressure")
    for node in nodes:
        conditions = {item["type"]: item["status"] for item in node["status"]["conditions"] if item["type"] in fields}
        if set(conditions) != set(fields):
            raise ValueError("node health conditions were incomplete")
        retained.append(conditions)
    healthy = all(node["Ready"] == "True" and all(node[field] == "False" for field in fields[1:]) for node in retained)
    return {"available": True, "observed_after_failed_startup": True, "healthy": healthy, "conditions": retained}


def tool_wrapper(tool, arguments, metadata_path=None):
    if metadata_path is not None:
        if tool != "trivy":
            raise ValueError("private scanner metadata is limited to Trivy")
        metadata = scanner_metadata(metadata_path)
        case, root = metadata["case"], Path(metadata["root"])
        real = metadata["tools"]["trivy"]
        real_docker = metadata["tools"]["docker"]
    else:
        guards.require_runner()
        case = os.environ["CF_OPERATIONAL_CASE"]
        root = Path(os.environ["CF_OPERATIONAL_ROOT"])
        if case not in CASES or root.is_symlink() or not root.name.startswith("cloudforge-operational-"):
            raise ValueError("operational fault requires its owned public fixture")
        fixture = root / "app"
        expected = json.loads((root / "source.json").read_text())
        if guards.hashes(fixture) != expected:
            raise ValueError("public operational fixture changed")
        real = os.environ["CF_OPERATIONAL_REAL_" + tool.upper()]
        real_docker = os.environ["CF_OPERATIONAL_REAL_DOCKER"]
    inject = None
    if case == "cleanup-inventory" and tool == "k3d" and arguments[:2] == ["cluster", "delete"]:
        if len(arguments) != 3 or not re.fullmatch(r"cloudforge-[a-f0-9]{8,32}", arguments[2]):
            raise ValueError("cleanup injection refused an unrelated cluster")
        result = subprocess.run([real, *arguments], check=False)
        if result.returncode == 0:
            (root / "cluster-deleted").write_text(arguments[2])
        return result.returncode
    if (case == "cleanup-inventory" and tool == "docker" and arguments[:2] == ["container", "ls"]
            and (root / "cluster-deleted").exists()
            and "name=k3d-" + (root / "cluster-deleted").read_text() in arguments):
        (root / "injected.json").write_text(json.dumps({"case": case, "scope": "bundled_public_fixture", "tool": tool}) + "\n")
        sys.stdout.write("a" * 64 + " k3d-cloudforge-incomplete")  # Deliberately no record terminator.
        return 0
    if tool == "docker" and case == "docker-unavailable":
        inject = "Docker daemon unavailable"
    if tool == "docker" and arguments[:2] == ["buildx", "build"] and case in ("disk-full", "registry-503"):
        inject = "no space left on device" if case == "disk-full" else "registry infrastructure returned 503 Service Unavailable"
    if tool == "trivy" and arguments[:1] == ["image"] and case.startswith("scanner-"):
        image = arguments[-1]
        if not re.fullmatch(r"cloudforge/healthy-node-api:[a-f0-9]{8,32}-a", image):
            raise ValueError("scanner injection refused an unrelated image")
        (root / "injected.json").write_text(json.dumps({"case": case, "scope": "bundled_public_fixture", "tool": tool}) + "\n")
        if case == "scanner-exit":
            print("intentional public scanner execution failure", file=sys.stderr)
            return 70
        observed = subprocess.check_output([real_docker, "image", "inspect", "--format", "{{.Id}}", image], text=True, timeout=15).strip()
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", observed):
            raise ValueError("public image identity unavailable")
        print(trivy_observation(case, image, observed))
        return 0
    if tool == "kubectl" and case in ("node-observation", "startup-failure"):
        if "rollout" in arguments and "status" in arguments and any(item.startswith("deployment/cf-healthy-node-api-") for item in arguments):
            result = subprocess.run([real, *arguments], check=False)
            if result.returncode:
                (root / "startup-failure-observed").write_text("true")
            return result.returncode
        if "get" in arguments and "nodes" in arguments and "json" in arguments and (root / "startup-failure-observed").exists():
            if case == "startup-failure":
                result = subprocess.run([real, *arguments], capture_output=True, check=False)
                sys.stdout.buffer.write(result.stdout)
                sys.stderr.buffer.write(result.stderr)
                observation = {"available": False, "observed_after_failed_startup": True, "healthy": False}
                if result.returncode == 0:
                    try:
                        observation = node_health_observation(result.stdout)
                    except (ValueError, KeyError, TypeError):
                        pass
                (root / "node-health.json").write_text(json.dumps(observation, indent=2, sort_keys=True) + "\n")
                return result.returncode
            (root / "injected.json").write_text(json.dumps({"case": case, "scope": "bundled_public_fixture", "tool": tool}) + "\n")
            print('{"items":')
            return 0
    if inject is not None:
        (root / "injected.json").write_text(json.dumps({"case": case, "scope": "bundled_public_fixture", "tool": tool}) + "\n")
        print(inject, file=sys.stderr)
        return 70
    os.execv(real, [real, *arguments])


def plant_rollout_failure(app):
    server = app / "server.js"
    source = server.read_text()
    if source.count('const failure = "none"') != 1:
        raise ValueError("public rollout fixture failure marker changed")
    server.write_text(source.replace('const failure = "none"', 'const failure = "rollout"'))
    # The healthy fixture does not consume the public version build argument.
    # Reuse the planted-rollout fixture's ARG/ENV propagation for image B.
    shutil.copyfile(ROOT / "testdata/broken-rollout/Dockerfile", app / "Dockerfile")


def main():
    if sys.argv[1:2] == ["__trivy"]:
        try:
            if len(sys.argv) < 3:
                raise ValueError("scanner metadata path is missing")
            return tool_wrapper("trivy", sys.argv[3:], sys.argv[2])
        except (guards.QualificationError, OSError, ValueError, KeyError, TypeError):
            print("Public operational scanner metadata could not be validated.", file=sys.stderr)
            return 2
    if sys.argv[1:2] == ["__tool"]:
        return tool_wrapper(sys.argv[2], sys.argv[3:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--case", required=True, choices=CASES)
    args = parser.parse_args()
    guards.require_runner()
    args.output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="cloudforge-operational-") as temporary:
        root = Path(temporary)
        app = root / "app"
        shutil.copytree(ROOT / "testdata/healthy-node", app)
        original_hashes = guards.hashes(app)
        # Keep runtime checks bounded and independent of experimental controls/HPA.
        config = app / "cloudforge.yaml"
        config.write_text("schema_version: v1alpha1\nruntime: {port: 8080}\nendpoints: {health: /health, readiness: /ready}\n")
        manifest = app / "k8s/app.yaml"
        manifest.write_text(manifest.read_text().split("---")[0])
        if args.case in ("node-observation", "startup-failure"):
            server = app / "server.js"
            server.write_text(server.read_text().replace("let ready = true", "let ready = false"))
        if args.case == "startup-failure":
            (root / "injected.json").write_text(json.dumps({"case": args.case, "scope": "copied_bundled_public_fixture",
                "type": "recorded_source_change", "file": "server.js", "change": "readiness remains false; runtime observations are unchanged"}) + "\n")
        if args.case == "cleanup-inventory":
            plant_rollout_failure(app)
        source_hashes = guards.hashes(app)
        (root / "source.json").write_text(json.dumps(source_hashes))
        (args.output / "fault-policy.json").write_text(json.dumps({"case": args.case, "scope": "copied_bundled_public_fixture", "retries": 0,
            "original_source_hashes": original_hashes, "selected_fixture_hashes": source_hashes,
            "changed_files": sorted(name for name in source_hashes if original_hashes.get(name) != source_hashes[name])}, indent=2, sort_keys=True) + "\n")
        bin_dir = root / "bin"
        bin_dir.mkdir()
        environment = dict(os.environ, CF_OPERATIONAL_ROOT=str(root), CF_OPERATIONAL_CASE=args.case, PATH=str(bin_dir) + os.pathsep + os.environ["PATH"], TMPDIR=str(root))
        for tool in ("docker", "trivy", "kubectl", "k3d"):
            environment["CF_OPERATIONAL_REAL_" + tool.upper()] = shutil.which(tool) or ""
            if not environment["CF_OPERATIONAL_REAL_" + tool.upper()]:
                raise ValueError("required runtime tool unavailable before injection")
        metadata = create_scanner_metadata(root, args.case,
            {tool: environment["CF_OPERATIONAL_REAL_" + tool.upper()] for tool in ("trivy", "docker")}, os.environ)
        for tool in ("docker", "trivy", "kubectl", "k3d"):
            wrapper = bin_dir / tool
            write_tool_wrapper(wrapper, tool, metadata)
        try:
            result = run_observed([str(args.binary.resolve()), "verify", str(app), "--format", "json"], args.output / "verification.json", timeout=1100, env=environment)
        finally:
            if (root / "injected.json").exists():
                shutil.copyfile(root / "injected.json", args.output / "injection-observed.json")
            if (root / "node-health.json").exists():
                shutil.copyfile(root / "node-health.json", args.output / "node-health.json")
        if not (args.output / "injection-observed.json").exists():
            raise ValueError("declared operational fault was never exercised")
        report = json.loads(result.stdout)
        qualify(report, result.returncode, args.case)
        if args.case == "startup-failure":
            observed = json.loads((args.output / "node-health.json").read_text())
            assert observed["available"] and observed["observed_after_failed_startup"] and observed["healthy"], observed
        assert not list(root.glob("cloudforge-verify-*")), "private runtime directory remained"
    subprocess.run(["bash", str(ROOT / "scripts/pilot-cleanup-check.sh")], check=True, timeout=60)
    print(f"{args.case}: original outcome and operational classification retained")
    return 0


if __name__ == "__main__":
    sys.exit(main())
