#!/usr/bin/env python3
"""Compare one public rate-limited fixture under two explicit test policies."""
from qualification_record import observation_started, observation_finished
import argparse
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
SOURCE = REPOSITORY / "testdata/rate-limited-http"
PREFIX = "CF_PROBE_PACING_"
VERIFY_SECONDS = 900
CLEANUP_SECONDS = 720
LIFECYCLE = ("graceful-shutdown", "pod-recovery", "rolling-deployment")
SPEC = importlib.util.spec_from_file_location("public_cancellation", REPOSITORY / "scripts/pilot-cancellation.py")
cancellation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(cancellation)
helpers = cancellation.helpers


def require(condition, message):
    if not condition:
        raise helpers.QualificationError(message)


def public_source():
    require(not SOURCE.is_symlink() and SOURCE.resolve() == SOURCE, "pacing_fixture_must_be_public_regular_source")
    for path in SOURCE.rglob("*"):
        require(stat.S_ISREG(path.lstat().st_mode) or stat.S_ISDIR(path.lstat().st_mode), "pacing_fixture_nonregular_file")
    return helpers.hashes(SOURCE)


def build_identity(arguments):
    """Only observe the known public image and its matching owned builder."""
    if arguments[:2] != ["buildx", "build"]:
        return None
    require(len(arguments) >= 8 and arguments[2] == "--builder", "pacing_observer_unknown_build")
    owner = arguments[3]
    # Current public IDs have 20 hex characters; retain historical fixture IDs.
    match = re.fullmatch(r"cloudforge-([a-f0-9]{8,32})", owner)
    require(match is not None, "pacing_observer_unknown_owner")
    require(arguments[4:8] == ["--load", "--provenance=false", "--label", "cloudforge.dev/run-id=" + owner], "pacing_observer_missing_ownership")
    require(arguments.count("--tag") == 1, "pacing_observer_unknown_tag")
    index = arguments.index("--tag")
    require(index + 1 < len(arguments), "pacing_observer_missing_tag")
    image = arguments[index + 1]
    require(image in ["cloudforge/rate-limited-http:" + match[1] + "-" + version for version in ("a", "b")], "pacing_observer_foreign_image")
    suffix = ["--tag", image, "."]
    remaining = arguments[8:]
    if remaining[:1] == ["--label"]:
        require(len(remaining) > 1 and re.fullmatch(r"cloudforge.dev/ownership=[a-f0-9]{32}", remaining[1]), "pacing_observer_missing_ownership")
        remaining = remaining[2:]
        require(remaining[:2] == ["--label", "cloudforge.dev/build-version=" + image[-1]], "pacing_observer_missing_build_identity")
        remaining = remaining[2:]
    require(remaining in (suffix, ["--build-arg", "CLOUDFORGE_VERSION=" + image[-1], *suffix]), "pacing_observer_unknown_build_arguments")
    return owner, image


def docker_wrapper(arguments):
    helpers.require_runner()
    real = os.environ[PREFIX + "REAL_DOCKER"]
    identity = build_identity(arguments)
    if identity is None:
        os.execv(real, [real, *arguments])
    require(Path.cwd().resolve() == SOURCE.resolve(), "pacing_observer_foreign_context")
    before = public_source()
    process = subprocess.Popen([real, *arguments])
    code = process.wait()
    if code == 0:
        owner, image = identity
        record = {"image": image, "owner": owner, "scope": "bundled_public_rate_limited_fixture_only",
                  "build_exit_code": code, "observed": False, "observation_timeout_seconds": 5,
                  "metadata_overhead_included_in_build_duration": True, "retry_performed": False}
        try:
            require(public_source() == before, "pacing_source_changed_during_build")
            result = helpers.capture([real, "image", "inspect", "--format", "{{json .RootFS.Layers}}", image], 5, limit=16384)
            layers = json.loads(result.stdout)
            require(result.returncode == 0 and isinstance(layers, list) and 0 < len(layers) <= 64
                    and all(isinstance(layer, str) and re.fullmatch(r"sha256:[a-f0-9]{64}", layer) for layer in layers), "pacing_image_filesystem_unavailable")
            record.update(observed=True, layers=layers)
        except (OSError, ValueError, subprocess.SubprocessError, helpers.QualificationError):
            record["observation_error"] = "public_image_filesystem_observation_failed"
        try:
            destination = Path(os.environ[PREFIX + "IMAGE_OUTPUT"])
            destination.mkdir(parents=True, exist_ok=True)
            helpers.write_json(destination / (image.rsplit(":", 1)[1] + ".json"), record)
        except OSError:
            pass  # The gate rejects absent proof; preserve the original build result.
    return cancellation.finish_signal(code)


def observed_429(report):
    return sum(int(m["value"]) for item in report["evidence"] for m in item.get("measurements", [])
               if m["name"] == "probe_http_status_429")


def qualify(report, code, paced):
    require(report["schema_version"] == "v1alpha8" and report["producer"]["commit"] not in ("", "unknown"), "pacing_missing_report_identity")
    require(code in (0, 1) and report["status"] in ("pass", "warn", "fail"), "pacing_native_execution_did_not_complete")
    policy = {"interval": "2s"} if paced else None
    require(report["plan"].get("probes") == policy and report["fingerprint"]["configuration"].get("probes") == policy, "pacing_effective_policy_mismatch")
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    require(not any(item["status"] == "error" for item in evidence.values()), "pacing_native_execution_error")
    if not paced:
        require(observed_429(report) > 0 and code == 1 and report["status"] == "fail", "pacing_default_429_failure_not_observed")
        require(any(evidence[name]["status"] == "fail" and evidence[name]["execution"]["executed"] for name in LIFECYCLE), "pacing_default_missing_lifecycle_failure")
        return
    require(observed_429(report) == 0, "pacing_explicit_policy_still_received_429")
    for name in ("container-build", "deployment-readiness", "semantic-readiness"):
        require(evidence[name]["status"] == "pass", "pacing_explicit_readiness_not_established")
    for name in LIFECYCLE:
        item = evidence[name]
        require(item["status"] in ("pass", "fail") and item["execution"]["executed"], "pacing_lifecycle_observation_missing")
        require(item["recovery"]["status"] == "pass" and all(check["status"] == "pass" for check in item["recovery"]["checks"]), "pacing_baseline_not_restored")
    if any(item["status"] == "fail" for item in evidence.values()):
        require(code == 1 and report["status"] == "fail", "pacing_original_failure_was_erased")


def retain_case_cleanup(output, root, before):
    limit = 65536
    record = {"exit_code": None, "completion_observed": False, "stream_limit_bytes": limit,
              "private_workspace_removed": None, "source_unchanged": None}
    stdout, stderr = b"", b""
    try:
        result = subprocess.run(["bash", str(REPOSITORY / "scripts/pilot-cleanup-check.sh")], capture_output=True, timeout=30)
        stdout, stderr = result.stdout, result.stderr
        record.update(exit_code=result.returncode, completion_observed=True)
    except subprocess.TimeoutExpired as error:
        stdout, stderr = error.stdout or b"", error.stderr or b""
        record.update(failure="timeout", observer_exit_code=124)
    except OSError:
        record.update(failure="spawn_error")
    for name, content in (("stdout", stdout), ("stderr", stderr)):
        if isinstance(content, str):
            content = content.encode()
        (output / ("cleanup." + name + ".txt")).write_bytes(content[-limit:])
        record[name + "_observed_bytes"] = len(content)
        record[name + "_truncated"] = len(content) > limit
    try:
        record["private_workspace_removed"] = not list(root.glob("cloudforge-verify-*"))
    except OSError:
        pass
    try:
        record["source_unchanged"] = public_source() == before
    except (OSError, helpers.QualificationError):
        pass
    record["passed"] = (record["completion_observed"] and record["exit_code"] == 0
                        and record["private_workspace_removed"] is True and record["source_unchanged"] is True)
    helpers.write_json(output / "cleanup.json", record)
    return record


def verify_case(binary, output, paced, before):
    output.mkdir()
    config = output / "runtime.yaml"
    config.write_text((SOURCE / "cloudforge.yaml").read_text() + ("\nprobes:\n  interval: 2s\n" if paced else ""))
    command = [binary, "verify", str(SOURCE), "--config", str(config), "--format", "json"]
    helpers.write_json(output / "command.json", {"argv": command, "verification_timeout_seconds": VERIFY_SECONDS,
        "outer_cleanup_wait_seconds": CLEANUP_SECONDS, "changed_setting": "probes.interval"})
    planned = subprocess.run([*command, "--plan"], capture_output=True, timeout=30)
    (output / "plan.json").write_bytes(planned.stdout)
    (output / "plan.stderr.txt").write_bytes(planned.stderr)
    require(planned.returncode == 0, "pacing_plan_blocked")
    repeated = subprocess.run([*command, "--plan"], capture_output=True, timeout=30)
    require(repeated.returncode == 0 and repeated.stdout == planned.stdout, "pacing_plan_not_deterministic")
    with tempfile.TemporaryDirectory(prefix="cf-probe-pacing-") as temporary:
        root = Path(temporary)
        bin_dir = root / "bin"
        bin_dir.mkdir()
        real_docker = shutil.which("docker")
        require(real_docker is not None, "pacing_docker_missing")
        wrapper = bin_dir / "docker"
        wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable,os.environ['CF_PROBE_PACING_HARNESS'],'__docker',*sys.argv[1:]])\n")
        wrapper.chmod(0o700)
        environment = dict(os.environ, TMPDIR=str(root), PATH=str(bin_dir) + os.pathsep + os.environ["PATH"])
        environment.update({PREFIX + "REAL_DOCKER": real_docker, PREFIX + "HARNESS": str(Path(__file__).resolve()), PREFIX + "IMAGE_OUTPUT": str(output / "image-filesystems")})
        process, interrupted_at = None, None
        try:
            with (output / "stdout.json").open("wb") as stdout, (output / "stderr.txt").open("wb") as stderr:
                observation_started(output / "stdout.json")
                process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=environment)
                try:
                    code = process.wait(timeout=VERIFY_SECONDS)
                except subprocess.TimeoutExpired:
                    interrupted_at = time.monotonic()
                    process.send_signal(signal.SIGINT)
                    process.wait(timeout=CLEANUP_SECONDS)
                    raise helpers.QualificationError("pacing_verification_outer_timeout")
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
            observation_finished(output / "stdout.json", process.returncode if process else None)
            helpers.write_json(output / "exit.json", {"exit_code": process.returncode if process else None})
            cleanup = retain_case_cleanup(output, root, before)
        require(cleanup["passed"], "pacing_owned_cleanup_or_source_proof_failed")
    report = json.loads((output / "stdout.json").read_text())
    for format_name, filename in (("text", "report.txt"), ("markdown", "report.md"), ("json", "canonical.json")):
        rendered = subprocess.run([binary, "report", str(output / "stdout.json"), "--format", format_name], capture_output=True, timeout=30)
        (output / filename).write_bytes(rendered.stdout)
        (output / (filename + ".stderr.txt")).write_bytes(rendered.stderr)
        require(rendered.returncode == 0, "pacing_report_loading_failed")
    qualify(report, code, paced)
    return report


def main():
    if sys.argv[1:2] == ["__docker"]:
        return docker_wrapper(sys.argv[2:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    helpers.require_runner()
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    binary = str(Path(args.binary).resolve())
    before = public_source()
    helpers.write_json(args.output / "source-hashes.json", before)
    reports = [verify_case(binary, args.output / name, paced, before) for name, paced in (("default", False), ("interval-2s", True))]
    configs = [dict(report["fingerprint"]["configuration"]) for report in reports]
    for config in configs:
        config.pop("probes", None)
    require(configs[0] == configs[1], "pacing_other_effective_configuration_changed")
    require(reports[0]["producer"] == reports[1]["producer"], "pacing_verifier_changed")
    require(reports[0]["fingerprint"]["compatibility_key"] != reports[1]["fingerprint"]["compatibility_key"], "pacing_baseline_compatibility_did_not_change")
    filesystems = [json.loads(path.read_text()) for path in sorted(args.output.glob("*/image-filesystems/*.json"))]
    for name, suffixes in (("default", ("a",)), ("interval-2s", ("a", "b"))):
        for suffix in suffixes:
            require(list((args.output / name / "image-filesystems").glob("*-" + suffix + ".json")), "pacing_required_image_contents_missing")
    require(len(filesystems) >= 3 and all(item["observed"] for item in filesystems), "pacing_image_contents_not_observed")
    require(all(item["layers"] == filesystems[0]["layers"] for item in filesystems), "pacing_application_image_contents_changed")
    require(public_source() == before, "pacing_source_changed")
    helpers.write_json(args.output / "qualification.json", {"qualified": True, "source_unchanged": True,
        "only_configuration_difference": "probes.interval", "same_image_filesystem": True,
        "image_ids": [report["fingerprint"]["image_id"] for report in reports],
        "image_identity_note": "Per-run ownership labels change whole image IDs; pinned base and filesystem layer hashes are compared.",
        "default_status": reports[0]["status"], "spaced_status": reports[1]["status"],
        "default_observed_429": observed_429(reports[0]), "spaced_observed_429": observed_429(reports[1]),
        "comparison_kind": "paired_observations_under_different_probe_policies_not_regression_grading"})
    print("Probe policies qualified; native failures preserved; same application filesystem; owned cleanup passed", flush=True)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except helpers.QualificationError as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
