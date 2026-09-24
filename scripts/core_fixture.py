"""Explicit HTTP-core test profiles; original public fixtures remain unchanged."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil

ROOT = Path(__file__).resolve().parent.parent
FIXTURES = {"healthy-node", "healthy-python", "healthy-node-redis", "healthy-python-redis"}
CONTROL = "experiments:\n  control_path: /_test\n"
EXPERIMENTAL = ("horizontal-autoscaling", "readiness-gating", "inflight-shutdown")
CORE_HEALTHY = ("container-build", "deployment-readiness", "graceful-shutdown", "pod-recovery",
                "rolling-deployment", "load-profile", "environment-cleanup")


def hashes(root):
    if root.is_symlink() or not root.is_dir():
        raise ValueError("public fixture must be a regular directory")
    values = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise ValueError("public fixture must not contain symbolic links")
        if path.is_file():
            values[path.relative_to(root).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
        elif not path.is_dir():
            raise ValueError("public fixture contains a nonregular entry")
    return values


def prepare(name, destination, record):
    if name not in FIXTURES:
        raise ValueError("unknown public HTTP-core fixture")
    source = ROOT / "testdata" / name
    destination, record = Path(destination), Path(record)
    if destination.exists() or destination.is_symlink() or record.exists() or record.is_symlink():
        raise ValueError("profile output already exists; preserve the earlier attempt")
    if (destination.resolve().is_relative_to(source.resolve()) or record.resolve().is_relative_to(source.resolve())
            or record.resolve().is_relative_to(destination.resolve())):
        raise ValueError("profile outputs must be outside the original and copied fixture")
    original = hashes(source)
    shutil.copytree(source, destination)
    config = destination / "cloudforge.yaml"
    value = config.read_text()
    if value.count(CONTROL) != 1:
        raise ValueError("public fixture control configuration changed; review the core profile")
    config.write_text(value.replace(CONTROL, ""))
    removed = []
    for path in sorted(destination.rglob("*.yaml")):
        if path == config:
            continue
        documents = re.split(r"(?m)^---\s*\n", path.read_text())
        selected = [doc for doc in documents if not re.search(r"(?m)^kind: HorizontalPodAutoscaler\s*$", doc)]
        if len(selected) != len(documents):
            removed.append(path.relative_to(destination).as_posix())
            path.write_text("---\n".join(selected))
    effective = hashes(destination)
    changed = sorted(key for key in original if original[key] != effective.get(key))
    if set(original) != set(effective) or set(changed) != {"cloudforge.yaml", *removed} or hashes(source) != original:
        raise ValueError("core profile changed application code or original fixture")
    value = {"schema_version": "qualification-profile-v1", "scope": "stable-http-core",
             "fixture": name, "configuration_origin": "explicit_test_configuration",
             "changes": {"removed_hpa_from": removed, "removed_control_path": "/_test"},
             "excluded_experiments": list(EXPERIMENTAL), "changed_files": changed,
             "source_hashes": original, "effective_hashes": effective,
             "application_and_build_files_unchanged": True}
    record.parent.mkdir(parents=True, exist_ok=True)
    record.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    return destination


def require_experimental_skipped(evidence):
    for name in EXPERIMENTAL:
        item = evidence[name]
        execution = item.get("execution", {})
        assert (item["status"] == "skipped" and execution.get("executed") is False
                and execution.get("mutation_attempted", False) is False), item


def require_executed(evidence, name, statuses=("pass",)):
    item = evidence[name]
    assert item["status"] in statuses and item.get("execution", {}).get("executed") is True, item


def require_core_scope(report):
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    assert len(evidence) == len(report["evidence"]), "Duplicate experiment evidence"
    require_experimental_skipped(evidence)
    require_executed(evidence, "environment-cleanup")
    return evidence


def require_core_healthy(report, exit_code, redis=False):
    assert exit_code == 0 and report["status"] in ("pass", "warn"), report.get("diagnostics")
    evidence = require_core_scope(report)
    assert all(item["status"] in ("pass", "warn", "skipped") for item in evidence.values()), evidence
    required = CORE_HEALTHY + (("dependency.redis", "semantic-readiness") if redis else ())
    for name in required:
        require_executed(evidence, name)
    require_executed(evidence, "container-scan", ("pass", "warn"))
    for name in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
        item = evidence[name]
        assert item["execution"].get("mutation_attempted") is True, item
        recovery = item.get("recovery", {})
        assert recovery.get("status") == "pass" and recovery.get("checks"), item
        assert all(check["status"] == "pass" for check in recovery["checks"]), item
    return evidence


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("fixture", choices=sorted(FIXTURES))
    parser.add_argument("destination", type=Path)
    parser.add_argument("record", type=Path)
    args = parser.parse_args()
    prepare(args.fixture, args.destination, args.record)
