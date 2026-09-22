#!/usr/bin/env python3
"""Run bounded reference trials and publish measurements, not a stability claim."""
import argparse
import hashlib
import json
from pathlib import Path
import statistics
import subprocess
from qualification_command import run_observed

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("fixture", choices=["healthy-node", "healthy-python"])
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=True)
source = Path("testdata")
def snapshot():
    return {str(path.relative_to(source)): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(source.rglob("*")) if path.is_file()}
before = snapshot()
metrics = {}
fingerprints = set()
for number in range(1, 6):
    path = args.output / f"{args.fixture}-{number}.json"
    result = run_observed([args.binary, "verify", f"testdata/{args.fixture}", "--format", "json"], path, timeout=900)
    report = json.loads(result.stdout)
    print(f"{args.fixture} trial {number}: exit={result.returncode} status={report['status']}", flush=True)
    assert result.returncode == 0, json.dumps(report.get("diagnostics"))
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    for experiment in ["container-build", "deployment-readiness", "readiness-gating", "inflight-shutdown", "pod-recovery", "rolling-deployment", "load-profile"]:
        assert evidence[experiment]["status"] == "pass", evidence[experiment]
    assert any(item["name"] == "sigterm_received" and item["value"] == "true" for item in evidence["inflight-shutdown"]["measurements"]), "Missing explicit SIGTERM overlap"
    assert evidence["environment-cleanup"]["status"] == "pass", evidence["environment-cleanup"]
    assert before == snapshot(), "Fixture source changed during qualification"
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
    result = run_observed([args.binary, "verify", f"testdata/{fixture}", "--format", "json"], args.output / f"{fixture}.json", timeout=900)
    report = json.loads(result.stdout)
    expected = "rolling-deployment" if "rollout" in fixture else "readiness-gating" if "readiness" in fixture else "inflight-shutdown"
    assert result.returncode == 1, report
    assert any(item["experiment_id"] == "environment-cleanup" and item["status"] == "pass" for item in report["evidence"]), "Native cleanup did not pass"
    assert before == snapshot(), "Fixture source changed during qualification"
    assert any(item["experiment_id"] == expected and item["status"] == "fail" for item in report["evidence"]), report
    print(f"{fixture}: expected {expected} failure detected", flush=True)
