#!/usr/bin/env python3
"""Validate selected-workload runtime evidence on a disposable runner."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("report", type=Path)
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=True)
root = Path("testdata/monorepo").resolve()
snapshot = lambda: {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(root.rglob("*")) if p.is_file()}
before = snapshot()
expected = {"app": "apps/http", "dockerfile": "apps/http/Containerfile.release", "context": "."}
report = json.loads(args.report.read_text())
assert report["schema_version"] == "v1alpha5"
assert report["plan"]["build"] == expected
assert report["fingerprint"]["configuration"]["build"] == expected
assert report["plan"]["topology"]["origin"] == "source"
assert report["status"] in ("pass", "warn"), report["status"]
evidence = {e["experiment_id"]: e for e in report["evidence"]}
for name in ("container-build", "deployment-readiness", "graceful-shutdown", "pod-recovery", "rolling-deployment"):
    assert evidence[name]["status"] == "pass", evidence[name]
    assert evidence[name]["execution"]["executed"], name
assert evidence["container-scan"]["status"] in ("pass", "warn")
for name in ("load-profile", "horizontal-autoscaling", "inflight-shutdown", "readiness-gating"):
    assert evidence[name]["status"] == "skipped", evidence[name]
for name in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
    assert evidence[name]["recovery"]["status"] == "pass", evidence[name]
# The fixture's readiness contract requires a value imported from the sibling package.
assert report["fingerprint"]["configuration"]["readiness"]["json"]["shared"] == "shared-package"
rollout = {m["name"]: m["value"] for m in evidence["rolling-deployment"].get("measurements", [])}
assert rollout["source_version"] == "a" and rollout["target_version"] == "b"
for command in ("analyze", "verify"):
    arguments = [args.binary, command, str(root), "--format", "json"]
    if command == "verify":
        arguments.append("--plan")
    first = subprocess.check_output(arguments, timeout=30)
    second = subprocess.check_output(arguments, timeout=30)
    assert first == second and str(root).encode() not in first
    (args.output / f"{command}.json").write_bytes(first)
# An app-only context cannot build the shared workspace. Record a real build FAIL,
# never silently replace it with the repository root or start a cluster.
config = args.output / "wrong-context.yaml"
config.write_text((root / "cloudforge.yaml").read_text().replace("context: .", "context: apps/http"))
with (args.output / "wrong-context.json").open("w") as stdout:
    process = subprocess.run([args.binary, "verify", str(root), "--config", str(config), "--format", "json"], stdout=stdout, timeout=900)
assert process.returncode == 1
wrong = json.loads((args.output / "wrong-context.json").read_text())
assert wrong["status"] == "fail"
assert any(e["experiment_id"] == "container-build" and e["status"] == "fail" for e in wrong["evidence"])
assert not any(e["experiment_id"] == "deployment-readiness" and e.get("execution", {}).get("executed") for e in wrong["evidence"])
for file in (args.report, args.output / "wrong-context.json"):
    rendered = subprocess.check_output([args.binary, "report", str(file), "--format", "json"], timeout=30)
    assert rendered == subprocess.check_output([args.binary, "report", str(file), "--format", "json"], timeout=30)
assert before == snapshot(), "Repository inputs changed"
(args.output / "checks.json").write_text(json.dumps({"shared_package_readiness": True, "image_a_b": True, "source_unchanged": True, "deterministic_inspection": True, "wrong_context_build_failed": True}, indent=2) + "\n")
print("Selected workspace build, semantic readiness, A/B rollout, recovery, wrong-context failure and repeatable reports validated.")
