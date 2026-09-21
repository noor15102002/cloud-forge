#!/usr/bin/env python3
"""Compare explicit test topologies on unchanged generic monorepo inputs.

Application PASS/FAIL is measured, never asserted from replica count. Harness
success requires complete, trustworthy evidence, restoration and owned cleanup.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("output", type=Path)
args = parser.parse_args()
binary = str(Path(args.binary).resolve())
args.output.mkdir(parents=True, exist_ok=True)
source = Path("testdata/monorepo").resolve()


def snapshot():
    return {str(p.relative_to(source)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(source.rglob("*")) if p.is_file()}


before = snapshot()
results = []
for replicas in (1, 2):
    output = args.output / f"replicas-{replicas}"
    output.mkdir()
    config = output / "runtime.yaml"
    config.write_text((source / "cloudforge.yaml").read_text().replace("v1alpha3", "v1alpha4") +
                      f"\ntopology:\n  replicas: {replicas}\n  rollout:\n    strategy: rolling_update\n    max_unavailable: 0\n    max_surge: 1\n")
    command = [binary, "verify", str(source), "--config", str(config.resolve()), "--format", "json"]
    planned = subprocess.check_output([*command, "--plan"], timeout=30)
    assert planned == subprocess.check_output([*command, "--plan"], timeout=30)
    (output / "plan.json").write_bytes(planned)
    plan = json.loads(planned)
    assert plan["status"] == "pass" and plan["topology"]["origin"] == "explicit_test_configuration"
    assert plan["topology"]["replicas"] == replicas
    with tempfile.TemporaryDirectory(prefix="cf-explicit-topology-") as temporary:
        with (output / "report.json").open("w") as stdout, (output / "stderr.txt").open("w") as stderr:
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=dict(os.environ, TMPDIR=temporary))
            try:
                code = process.wait(timeout=900)
            except subprocess.TimeoutExpired:
                process.send_signal(signal.SIGINT)
                process.wait(timeout=720)
                raise
        report = json.loads((output / "report.json").read_text())
        assert report["schema_version"] == "v1alpha8" and report["producer"]["commit"] not in ("", "unknown")
        assert code in (0, 1) and report["status"] in ("pass", "warn", "fail")
        evidence = {e["experiment_id"]: e for e in report["evidence"]}
        for name in ("container-build", "deployment-readiness", "semantic-readiness"):
            assert evidence[name]["status"] == "pass", evidence[name]
        for name in ("graceful-shutdown", "pod-recovery", "rolling-deployment"):
            item = evidence[name]
            assert item["status"] in ("pass", "fail") and item["execution"]["executed"], item
            assert item["topology"]["replicas"] == replicas and item["topology"]["origin"] == "explicit_test_configuration"
            assert item["recovery"]["status"] == "pass", item
            assert all(c["status"] == "pass" for c in item["recovery"]["checks"])
            measurements = {m["name"]: m["value"] for m in item["measurements"]}
            assert "minimum_ready_pods" in measurements and "downtime_ms" in measurements
            assert measurements["final_http_status"] == "200"
        if any(e["status"] == "fail" for e in evidence.values()):
            assert report["status"] == "fail" and code == 1, "Restoration erased a failure"
        assert not list(Path(temporary).glob("cloudforge-verify-*")), "Workspace leak"
    subprocess.run(["bash", "scripts/pilot-cleanup-check.sh"], check=True, timeout=60)
    assert snapshot() == before, "Source changed"
    canonical = subprocess.check_output([binary, "report", str(output / "report.json"), "--format", "json"], timeout=30)
    assert canonical == subprocess.check_output([binary, "report", str(output / "report.json"), "--format", "json"], timeout=30)
    (output / "report.md").write_bytes(subprocess.check_output([binary, "report", str(output / "report.json"), "--format", "markdown"], timeout=30))
    results.append(report)
    print(f"{replicas} replicas: {report['status']}; complete evidence, restoration and cleanup", flush=True)

left, right = [r["fingerprint"] for r in results]
for field in ("source_commit", "source_dirty", "cloudforge_commit", "cloudforge_version", "tools", "resources", "budget", "platform", "cpus"):
    assert left[field] == right[field], field
configs = [dict(f["configuration"]) for f in (left, right)]
for config in configs:
    config["topology"] = dict(config["topology"], replicas=0)
assert configs[0] == configs[1], "Configuration changed beyond replicas"
assert left["compatibility_key"] != right["compatibility_key"], "Cross-topology baseline accepted"
summary = {"source_unchanged": True, "only_configuration_difference": "topology.replicas", "cleanup": True,
           "comparison_kind": "side_by_side_observations_not_regression_grading", "cases": []}
for replicas, report in zip((1, 2), results):
    summary["cases"].append({"replicas": replicas, "status": report["status"], "image_id": report["fingerprint"]["image_id"],
                             "evidence": [{"experiment": e["experiment_id"], "status": e["status"], "measurements": e.get("measurements", []), "recovery": e.get("recovery")}
                                          for e in report["evidence"]]})
(args.output / "comparison.json").write_text(json.dumps(summary, indent=2) + "\n")
