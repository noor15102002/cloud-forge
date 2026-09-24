#!/usr/bin/env python3
"""Retain the two existing integration failure cases before asserting their contract."""
import argparse
import json
from pathlib import Path
from qualification_command import run_observed
from core_fixture import require_core_scope, require_executed

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("binary")
parser.add_argument("output", type=Path)
parser.add_argument("--core", action="store_true", help="Qualify the stable rollout fault; in-flight shutdown stays experimental")
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=False)
cases = [("broken-rollout", "rolling-deployment")] if args.core else [("broken-shutdown", "inflight-shutdown"), ("broken-rollout", "rolling-deployment")]
for name, experiment in cases:
    result = run_observed([args.binary, "verify", "testdata/" + name, "--format", "json"], args.output / (name + ".json"), timeout=900)
    report = json.loads(result.stdout)
    assert result.returncode == 1 and report["status"] == "fail", report.get("diagnostics")
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    if args.core:
        require_core_scope(report)
        require_executed(evidence, experiment, ("fail",))
    assert evidence[experiment]["status"] == "fail", evidence[experiment]
    assert evidence["environment-cleanup"]["status"] == "pass", evidence["environment-cleanup"]
    if name == "broken-shutdown":
        assert any(item["id"] == "runtime.inflight-shutdown" and item["status"] == "fail" for item in report["findings"])
    else:
        assert any(item["name"] == "target_ready_pods" and item["value"] == "0" for item in evidence[experiment]["measurements"])
        assert any(item["code"] == "rolling_deployment_failed" and item["status"] == "fail" for item in report["diagnostics"])
