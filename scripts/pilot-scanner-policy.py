#!/usr/bin/env python3
"""Qualify real scanner isolation with one inert, pinned public vulnerable package.

Runtime execution is restricted to disposable hosted Linux runners. The bundled
HTTP application is unchanged; a verified npm package is copied into its image
solely as scanner input and is never imported, installed, or executed.
"""
import argparse
import base64
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

from core_fixture import prepare, hashes, require_core_healthy
from qualification_command import run_observed

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("scanner_guards", ROOT / "scripts/pilot-backend-cancellation.py")
guards = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guards)
PACKAGE_URL = "https://registry.npmjs.org/ip/-/ip-2.0.1.tgz"
PACKAGE_SHA256 = "1d125fef28131e1b20420637007785feaba79b6c84a0ac1cef2eb8cfb1cd43c9"
PACKAGE_INTEGRITY = "sha512-lJUL9imLTNi1ZfXT+DU6rBBdbiKGBuay9B6xGSPVjUeQwaH1RIGqef8RZkUtHioLmSNpPR5M4HVKJGm1j8FWVQ=="
ADVISORIES = ("CVE-2024-29415", "GHSA-2p57-rm9w-gvfp")
SCAN_LIMIT = 8 * 1024 * 1024
SEVERITIES = "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL"
POLICY = {"scan_policy": "cloudforge-default-v1", "scan_scanners": "vuln", "scan_severities": SEVERITIES,
          "scan_ignore_policy": "none", "scan_include_unfixed": "true", "scan_ambient_configuration": "disabled",
          "scan_ambient_ignore_files": "disabled", "scan_cache": "private_fresh", "scan_image_source": "docker"}
INVENTORY_ENTRIES = 256
INVENTORY_DEPTH = 8


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def runtime_inventory(root):
    """Bounded metadata only; never open file contents or follow symbolic links.

    Directory descriptors and O_NOFOLLOW keep concurrent replacement with a
    symlink from making this observer inspect an unrelated path. An incomplete
    observation cannot establish that the runtime directory is empty.
    """
    result = {"schema_version": "scanner-runtime-inventory-v1", "scope": "private_public_fixture_runtime_directory",
              "started_at": guards.observer_timestamp(), "entry_limit": INVENTORY_ENTRIES, "depth_limit": INVENTORY_DEPTH,
              "file_contents_read": False, "symlink_targets_read": False, "entries": [], "errors": [], "truncated": False}
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW

    def file_type(mode):
        for predicate, name in ((stat.S_ISREG, "regular"), (stat.S_ISDIR, "directory"), (stat.S_ISLNK, "symlink"),
                                (stat.S_ISFIFO, "fifo"), (stat.S_ISSOCK, "socket"), (stat.S_ISCHR, "character_device"), (stat.S_ISBLK, "block_device")):
            if predicate(mode):
                return name
        return "other"

    def visit(descriptor, prefix, depth):
        remaining = INVENTORY_ENTRIES - len(result["entries"])
        names = []
        try:
            with os.scandir(descriptor) as stream:
                for entry in stream:
                    if len(names) >= remaining:
                        result["truncated"] = True
                        break
                    names.append(entry.name)
        except OSError as error:
            result["errors"].append({"path": prefix or ".", "operation": "list_directory", "errno": error.errno})
            return
        children = []
        for name in sorted(names):
            path = prefix + "/" + name if prefix else name
            try:
                info = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
            except OSError as error:
                result["errors"].append({"path": path, "operation": "metadata", "errno": error.errno})
                continue
            kind = file_type(info.st_mode)
            result["entries"].append({"path": path, "type": kind, "size_bytes": info.st_size,
                                      "mode": oct(stat.S_IMODE(info.st_mode))})
            if kind == "directory":
                children.append((name, path))
        for name, path in children:
            if depth >= INVENTORY_DEPTH or len(result["entries"]) >= INVENTORY_ENTRIES:
                result["truncated"] = True
                continue
            child = None
            try:
                child = os.open(name, flags, dir_fd=descriptor)
                visit(child, path, depth + 1)
            except OSError as error:
                result["errors"].append({"path": path, "operation": "open_directory_nofollow", "errno": error.errno})
            finally:
                if child is not None:
                    os.close(child)

    descriptor = None
    try:
        descriptor = os.open(root, flags)
        visit(descriptor, "", 0)
    except OSError as error:
        result["errors"].append({"path": ".", "operation": "open_root_nofollow", "errno": error.errno})
    finally:
        if descriptor is not None:
            os.close(descriptor)
    result["complete"] = not result["errors"] and not result["truncated"]
    result["empty"] = result["complete"] and not result["entries"]
    result["finished_at"] = guards.observer_timestamp()
    return result


def extract_package(data, destination):
    if (len(data) > 64 * 1024 or hashlib.sha256(data).hexdigest() != PACKAGE_SHA256
            or "sha512-" + base64.b64encode(hashlib.sha512(data).digest()).decode() != PACKAGE_INTEGRITY):
        raise ValueError("pinned public scanner package integrity mismatch")
    destination = Path(destination)
    if destination.exists() or destination.is_symlink():
        raise ValueError("scanner package output already exists")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = archive.getmembers()
        if len(members) > 16 or sum(item.size for item in members) > 64 * 1024:
            raise ValueError("scanner package archive exceeds its bounds")
        selected = {}
        for item in members:
            parts = PurePosixPath(item.name).parts
            if (not item.isfile() or not parts or parts[0] != "package" or len(parts) < 2
                    or any(part in (".", "..", "") for part in parts) or PurePosixPath(item.name).is_absolute()):
                raise ValueError("scanner package archive has an unsafe entry")
            name = PurePosixPath(*parts[1:]).as_posix()
            if name in selected:
                raise ValueError("scanner package archive has duplicate entries")
            selected[name] = archive.extractfile(item).read()
        metadata = json.loads(selected.get("package.json", b"null"))
        if not isinstance(metadata, dict) or (metadata.get("name"), metadata.get("version")) != ("ip", "2.0.1"):
            raise ValueError("scanner package metadata does not match the pinned package")
        destination.mkdir(mode=0o700)
        for name, content in selected.items():
            path = destination / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
    return hashes(destination)


def prepare_fixture(root, output, package_bytes):
    app = prepare("healthy-node", root / "app", output / "core-profile.json")
    before = hashes(app)
    # This is an installed package, not another application root. Keeping its
    # ordinary node_modules path also preserves read-only workload selection.
    (app / "node_modules").mkdir()
    package = extract_package(package_bytes, app / "node_modules/ip")
    dockerfile = app / "Dockerfile"
    data = dockerfile.read_text()
    if data.count("USER node\n") != 1:
        raise ValueError("public scanner fixture Dockerfile shape changed")
    dockerfile.write_text(data.replace("USER node\n", "COPY --chown=node:node node_modules/ip/ /app/node_modules/ip/\nUSER node\n"))
    after = hashes(app)
    changed = sorted(name for name in set(before) | set(after) if before.get(name) != after.get(name))
    if any(name != "Dockerfile" and not name.startswith("node_modules/ip/") for name in changed):
        raise ValueError("scanner qualification changed application behavior")
    write_json(output / "scanner-fixture.json", {
        "schema_version": "scanner-policy-fixture-v1", "scope": "copied_public_http_core_fixture",
        "package": {"name": "ip", "version": "2.0.1", "url": PACKAGE_URL, "sha256": PACKAGE_SHA256,
                    "integrity": PACKAGE_INTEGRITY, "expected_advisory_aliases": list(ADVISORIES), "executed": False},
        "image_change": "copy verified package files to /app/node_modules/ip for real scanner detection only",
        "application_code_unchanged": before["server.js"] == after["server.js"],
        "before_hashes": before, "effective_hashes": after, "changed_files": changed, "package_file_hashes": package})
    return app, after


def known_findings(report):
    return [vulnerability for result in report.get("Results", []) or []
            for vulnerability in result.get("Vulnerabilities", []) or []
            if vulnerability.get("VulnerabilityID") in ADVISORIES
            and vulnerability.get("PkgName") == "ip" and vulnerability.get("InstalledVersion") == "2.0.1"]


def require_known_unfixed(report):
    findings = known_findings(report)
    if not findings or not all(item.get("Severity") == "HIGH" and not item.get("FixedVersion") for item in findings):
        raise ValueError("real scan did not retain the pinned HIGH unfixed package finding; preserve evidence and review database changes")
    return sorted({item["VulnerabilityID"] for item in findings})


def observed_policy(arguments, state):
    def argument(name):
        if arguments.count(name) != 1:
            raise ValueError("scanner policy flag absent or ambiguous")
        return arguments[arguments.index(name) + 1]
    directory = Path.cwd()
    if directory.parent != Path(state["runtime"]) or not directory.name.startswith("cloudforge-scan-"):
        raise ValueError("scanner did not use its private execution directory")
    config = Path(argument("--config"))
    if (config != directory / "trivy.yaml" or config.is_symlink() or not config.is_file()
            or config.read_bytes() != b"{}\n" or stat.S_IMODE(config.stat().st_mode) != 0o600):
        raise ValueError("scanner did not use its empty private configuration")
    if any(name.startswith("TRIVY_") for name in os.environ):
        raise ValueError("ambient scanner environment survived isolation")
    expected_environment = {"HOME": directory / "home", "XDG_CONFIG_HOME": directory / "config",
                            "XDG_CACHE_HOME": directory / "cache", "TMPDIR": directory / "temp"}
    if any(os.environ.get(name) != str(path) for name, path in expected_environment.items()):
        raise ValueError("scanner private environment mismatch")
    if (os.environ.get("DOCKER_HOST") != "unix:///var/run/docker.sock"
            or any(os.environ.get(name) for name in ("DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"))):
        raise ValueError("scanner did not retain the approved local Docker endpoint")
    value = {"schema_version": "scanner-invocation-policy-v1", "actual_real_binary": True,
             "ambient_trivy_variables_absent": True, "private_configuration": True, "private_environment": True,
             "pinned_local_docker_endpoint": True, "real_trivy_sha256": state["trivy_sha256"]}
    if "--version" not in arguments:
        if (argument("--scanners") != "vuln" or argument("--severity") != SEVERITIES
                or "--ignore-unfixed=false" not in arguments or argument("--image-src") != "docker"
                or argument("--cache-dir") != str(directory / "cache")):
            raise ValueError("scanner did not use the fixed vulnerability policy")
        ignore = Path(argument("--ignorefile"))
        if ignore != directory / ".trivyignore" or ignore.is_symlink() or not ignore.is_file() or ignore.read_bytes():
            raise ValueError("scanner did not use its empty private ignore file")
        value.update(policy=POLICY, empty_ignore_file=True)
    return value


def trivy_wrapper(state_path, arguments):
    state_path = Path(state_path)
    root = state_path.parent
    if (state_path.name != "state.json" or state_path.is_symlink() or not state_path.is_file()
            or not root.name.startswith("cloudforge-scanner-policy-") or root.resolve() != root
            or stat.S_IMODE(state_path.stat().st_mode) != 0o600):
        raise ValueError("scanner observer refused an unowned state file")
    state = json.loads(state_path.read_text())
    # The outer process validates these three actual runner fields before it
    # creates this private state. They are not added to the scanner environment.
    guards.require_runner(state["runner"])
    if hashes(root / "app") != state["source_hashes"]:
        raise ValueError("scanner observer refused a changed public fixture")
    real = Path(state["trivy"])
    if not real.is_file() or list((real.stat().st_dev, real.stat().st_ino, real.stat().st_size, real.stat().st_mtime_ns)) != state["trivy_stat"]:
        raise ValueError("scanner binary changed after its digest was recorded")
    policy = observed_policy(arguments, state)
    output = Path(state["output"])
    # Python may coerce its locale on startup. It is not a scanner setting and
    # was not passed by CloudForge's allowlist; do not inject it into real Trivy.
    os.environ.pop("LC_CTYPE", None)
    if arguments[:1] == ["--version"]:
        write_json(output / "version-policy.json", policy)
        os.execv(str(real), [str(real), *arguments])
    if (arguments[:1] != ["image"] or not re.fullmatch(r"cloudforge/healthy-node-api:[a-f0-9]{8,32}-a", arguments[-1])):
        raise ValueError("scanner observer refused an unrelated image command")
    write_json(output / "scan-policy.json", policy)
    guards.IMPORT_STREAM_LIMIT = SCAN_LIMIT
    return guards.tee_command([str(real), *arguments], output / "real-trivy", "image-scan", sys.stdout.buffer, sys.stderr.buffer,
                              scope="copied_public_scanner_fixture_only", tool="trivy", name="trivy-scan", native_timeout_seconds=360)


def read_real_observation(output):
    paths = list((output / "real-trivy").glob("trivy-scan-*.json"))
    if len(paths) != 1:
        raise ValueError("expected exactly one real image scan; retries are not allowed")
    observation = json.loads(paths[0].read_text())
    if (observation.get("exit_code") != 0 or observation.get("completion_observed") is not True
            or observation.get("actual_command_executed") is not True or observation.get("retry_performed") is not False
            or observation.get("artifact_write_error") or any(item["truncated"] for item in observation["streams"].values())):
        raise ValueError("real scanner observation was not complete and reliable")
    path = paths[0].parent / observation["streams"]["stdout"]["file"]
    raw = path.read_bytes()
    if len(raw) > SCAN_LIMIT:
        raise ValueError("real scanner report exceeded retained bound")
    return path, json.loads(raw)


def filter_demonstration(real_trivy, raw_path, root, output):
    directory = root / "filter-demonstration"
    directory.mkdir()
    (directory / "trivy.yaml").write_text("{}\n")
    (directory / "empty-ignore").write_text("")
    (directory / ".trivyignore").write_text("\n".join(ADVISORIES) + "\n")
    environment = {name: os.environ[name] for name in ("PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR") if name in os.environ}
    environment.update(HOME=str(directory), XDG_CONFIG_HOME=str(directory), XDG_CACHE_HOME=str(directory))
    for label, flags in (("critical-only", ["--severity", "CRITICAL", "--ignorefile", str(directory / "empty-ignore")]),
                         ("ignored-advisory", ["--severity", SEVERITIES, "--ignorefile", str(directory / ".trivyignore")])):
        path = output / f"real-filter-{label}.json"
        command = [real_trivy, "convert", "--format", "json", "--quiet", "--config", str(directory / "trivy.yaml"), *flags, str(raw_path)]
        result = guards.capture(command, 30, limit=SCAN_LIMIT, environment=environment)
        path.write_bytes(result.stdout)
        path.with_suffix(".stderr.txt").write_bytes(result.stderr)
        write_json(path.with_suffix(".exit.json"), {"exit_code": result.returncode, "real_trivy": True, "source": "same_retained_native_scan", "no_new_scan": True})
        if result.returncode != 0 or known_findings(json.loads(result.stdout)):
            raise ValueError("real Trivy did not demonstrate the expected caller-policy suppression")


def qualify(report, native_exit, raw_scan):
    evidence = require_core_healthy(report, native_exit)
    if report["status"] != "warn" or evidence["container-scan"]["status"] != "warn":
        raise ValueError("known vulnerability warning was not preserved in native evidence")
    measurements = {item["name"]: item["value"] for item in evidence["container-scan"]["measurements"]}
    if any(measurements.get(name) != value for name, value in POLICY.items()):
        raise ValueError("native report did not retain the effective scanner policy")
    if raw_scan.get("Metadata", {}).get("ImageID") != report["fingerprint"]["image_id"]:
        raise ValueError("retained real scan does not identify the native image")
    if {item["name"]: item["version"] for item in report["fingerprint"]["tools"]}.get("trivy") != "0.74.0":
        raise ValueError("scanner qualification requires the pinned real Trivy version")
    identities = require_known_unfixed(raw_scan)
    for identity in identities:
        if not any(item["status"] == "warn" and item["severity"] == "high" and item.get("observed") == f"{identity} in ip 2.0.1" for item in report["findings"]):
            raise ValueError("native normalized findings lost the known real vulnerability")
    return {"schema_version": "scanner-policy-qualification-v1", "status": "pass",
        "native_status": report["status"], "native_exit_code": native_exit, "known_advisories": identities,
        "package": "ip@2.0.1", "severity": "HIGH", "unfixed": True, "real_trivy": "0.74.0", "policy": POLICY,
        "caller_filters_proven_to_suppress_same_finding": ["critical-only", "ignored-advisory"],
        "native_finding_retained": True, "native_cleanup": evidence["environment-cleanup"]["status"], "retries": 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    guards.require_runner()
    binary, output = args.binary.resolve(), args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    original = hashes(ROOT / "testdata/healthy-node")
    real = Path(shutil.which("trivy") or "missing-trivy").resolve()
    if not real.is_file():
        raise ValueError("real Trivy unavailable before scanner qualification")
    with urllib.request.urlopen(PACKAGE_URL, timeout=30) as response:
        package_bytes = response.read(64 * 1024 + 1)
    with tempfile.TemporaryDirectory(prefix="cloudforge-scanner-policy-") as temporary:
        root = Path(temporary).resolve()
        app, selected = prepare_fixture(root, output, package_bytes)
        (output / "public-ip-2.0.1.tgz").write_bytes(package_bytes)
        runtime, caller, wrappers = root / "runtime", root / "caller", root / "bin"
        for directory in (runtime, caller, wrappers):
            directory.mkdir(mode=0o700)
        (caller / "trivy.yaml").write_text("severity: [CRITICAL]\nvulnerability: {ignore-unfixed: true}\n")
        (caller / ".trivyignore").write_text("\n".join(ADVISORIES) + "\n")
        (caller / "ignore.rego").write_text("package trivy\nignore = true\n")
        state = {"output": str(output), "runtime": str(runtime), "source_hashes": selected,
                 "runner": {name: os.environ[name] for name in ("GITHUB_ACTIONS", "RUNNER_ENVIRONMENT", "RUNNER_OS")},
                 "trivy": str(real), "trivy_sha256": hashlib.sha256(real.read_bytes()).hexdigest(),
                 "trivy_stat": list((real.stat().st_dev, real.stat().st_ino, real.stat().st_size, real.stat().st_mtime_ns))}
        state_path = root / "state.json"
        write_json(state_path, state)
        state_path.chmod(0o600)
        wrapper = wrappers / "trivy"
        wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable," + repr(str(Path(__file__).resolve())) + ", '__trivy', " + repr(str(state_path)) + ", *sys.argv[1:]])\n")
        wrapper.chmod(0o700)
        hostile = {"TRIVY_CONFIG": str(caller / "trivy.yaml"), "TRIVY_SEVERITY": "CRITICAL", "TRIVY_IGNORE_UNFIXED": "true",
                   "TRIVY_IGNOREFILE": str(caller / ".trivyignore"), "TRIVY_IGNORE_POLICY": str(caller / "ignore.rego"),
                   "TRIVY_IGNORE_STATUS": "affected", "TRIVY_SKIP_DIRS": "*", "TRIVY_FUTURE_FILTER": "hide-all"}
        environment = dict(os.environ, **hostile, TMPDIR=str(runtime), PATH=str(wrappers) + os.pathsep + os.environ["PATH"])
        write_json(output / "caller-policy.json", {"schema_version": "scanner-caller-policy-v1", "public_test_values_only": True,
            "variables": sorted(hostile), "severity": "CRITICAL", "ignore_unfixed": True,
            "ignore_advisory_aliases": list(ADVISORIES), "configuration_hashes": hashes(caller),
            "real_trivy_sha256": state["trivy_sha256"], "retries": 0})
        previous = Path.cwd()
        try:
            os.chdir(caller)
            result = run_observed([str(binary), "verify", str(app), "--format", "json"], output / "verification.json", timeout=1200, env=environment, own_session=True)
        finally:
            os.chdir(previous)
            inventory = runtime_inventory(runtime)
            write_json(output / "runtime-inventory.json", inventory)
            write_json(output / "temporary-cleanup.json", {"runtime_directory_empty": inventory["empty"],
                       "original_fixture_unchanged": hashes(ROOT / "testdata/healthy-node") == original,
                       "selected_fixture_unchanged": hashes(app) == selected})
        raw_path, raw_scan = read_real_observation(output)
        require_known_unfixed(raw_scan)
        filter_demonstration(str(real), raw_path, root, output)
        summary = qualify(json.loads(result.stdout), result.returncode, raw_scan)
        cleanup = json.loads((output / "temporary-cleanup.json").read_text())
        if not all(cleanup.values()):
            raise ValueError("scanner qualification changed source or leaked temporary runtime files")
        subprocess.run(["bash", str(ROOT / "scripts/pilot-cleanup-check.sh")], check=True, timeout=180)
        write_json(output / "scanner-policy-result.json", summary)
        print("scanner-policy: real HIGH unfixed finding retained; caller filters independently demonstrated; native cleanup PASS", flush=True)


if __name__ == "__main__":
    if sys.argv[1:2] == ["__trivy"]:
        raise SystemExit(trivy_wrapper(sys.argv[2], sys.argv[3:]))
    main()
