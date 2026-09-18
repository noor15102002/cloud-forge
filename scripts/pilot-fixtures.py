#!/usr/bin/env python3
"""Run bounded reference trials and publish measurements, not a stability claim."""
import argparse
import json
from pathlib import Path
import statistics
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("fixture", choices=["healthy-node", "healthy-python"])
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=True)
metrics = {}
fingerprints = set()
for number in range(1, 6):
    result = subprocess.run([args.binary, "verify", f"testdata/{args.fixture}", "--format", "json"], capture_output=True, text=True, timeout=900)
    path = args.output / f"{args.fixture}-{number}.json"
    path.write_text(result.stdout)
    report = json.loads(result.stdout)
    print(f"{args.fixture} trial {number}: exit={result.returncode} status={report['status']}", flush=True)
    assert result.returncode == 0, json.dumps(report.get("diagnostics"))
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    for experiment in ["container-build", "deployment-readiness", "readiness-gating", "inflight-shutdown", "pod-recovery", "rolling-deployment", "load-profile"]:
        assert evidence[experiment]["status"] == "pass", evidence[experiment]
    key = report["fingerprint"].get("compatibility_key")
    assert key, "Missing complete fingerprint"
    fingerprints.add(key)
    for experiment, item in evidence.items():
        for measurement in item.get("measurements", []):
            name = measurement["name"]
            if name in ["startup_duration_ms", "replacement_duration_ms", "rollout_duration_ms", "latency_p95_ms", "throughput_rps"]:
                metrics.setdefault(f"{experiment}.{name}", []).append(float(measurement["value"]))
    leaked = subprocess.check_output(["k3d", "cluster", "list", "--no-headers"], text=True)
    assert "cloudforge-" not in leaked, "Cluster leak"
assert len(fingerprints) == 1, "Repeated trials had different experiment environments"
summary = {name: {"samples": values, "min": min(values), "max": max(values), "mean": statistics.mean(values), "stdev": statistics.stdev(values)} for name, values in metrics.items()}
(args.output / "variance.json").write_text(json.dumps(summary, indent=2) + "\n")
print(json.dumps(summary, indent=2))

broken = ["broken-shutdown", "broken-rollout"] if args.fixture == "healthy-node" else ["broken-python-shutdown", "broken-python-readiness"]
for fixture in broken:
    result = subprocess.run([args.binary, "verify", f"testdata/{fixture}", "--format", "json"], capture_output=True, text=True, timeout=900)
    (args.output / f"{fixture}.json").write_text(result.stdout)
    report = json.loads(result.stdout)
    expected = "rolling-deployment" if "rollout" in fixture else "readiness-gating" if "readiness" in fixture else "inflight-shutdown"
    assert result.returncode == 1, report
    assert any(item["experiment_id"] == expected and item["status"] == "fail" for item in report["evidence"]), report
    print(f"{fixture}: expected {expected} failure detected", flush=True)
