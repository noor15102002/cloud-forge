#!/usr/bin/env python3
"""Verify two pinned public apps on disposable runners; never Peaxis or Avylo."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.binary = str(Path(args.binary).resolve())
args.output.mkdir(parents=True, exist_ok=True)
apps = [
    ("express", "https://github.com/dockersamples/node-bulletin-board.git", "4aaaa14f11cb9b45fe1a1b9db3edfdb5d9a4a13e", "bulletin-board-app", 8080),
    ("fastapi", "https://github.com/pixegami/simple-fastapi-example.git", "a6b5b44cf1a81ad026a213063c6b4e26373f1479", ".", 8000),
]
for name, url, revision, subdirectory, port in apps:
    with tempfile.TemporaryDirectory(prefix="cloudforge-external-") as temporary:
        repo = Path(temporary)
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        subprocess.run(["git", "-C", str(repo), "fetch", "--depth=1", url, revision], check=True, timeout=120)
        subprocess.run(["git", "-C", str(repo), "checkout", "--detach", "FETCH_HEAD"], check=True)
        app = repo / subdirectory
        # The FastAPI example does not ship a Dockerfile. Supply a standard test
        # package without editing its application source or adding core special cases.
        if name == "fastapi":
            (app / "Dockerfile").write_text('FROM python:3.12-slim\nWORKDIR /app\nCOPY requirements.txt .\nRUN pip install --no-cache-dir -r requirements.txt\nCOPY main.py .\nUSER 10001\nEXPOSE 8000\nCMD ["python","-m","uvicorn","main:app","--host","0.0.0.0","--port","8000"]\n')
        (app / "cloudforge.yaml").write_text(f"schema_version: v1alpha1\nruntime:\n  port: {port}\nendpoints:\n  health: /\n  readiness: /\n  load: /\nload:\n  vus: 5\n  duration: 20s\n")
        # Both apps lack a Kubernetes deployment. This explicit reference test
        # deployment requests two replicas; no existing source manifest is overwritten.
        deployment = {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "pilot"}, "spec": {"replicas": 2, "selector": {"matchLabels": {"app": "pilot"}}, "template": {"metadata": {"labels": {"app": "pilot"}}, "spec": {"terminationGracePeriodSeconds": 30, "containers": [{"name": "app", "image": "pilot:local", "ports": [{"name": "http", "containerPort": port}], "readinessProbe": {"httpGet": {"path": "/", "port": "http"}, "periodSeconds": 1}, "livenessProbe": {"httpGet": {"path": "/", "port": "http"}}, "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "500m", "memory": "256Mi"}}}]}}}}
        (app / "cloudforge-pilot-deployment.yaml").write_text(json.dumps(deployment))
        result = subprocess.run([args.binary, "verify", str(app), "--format", "json"], capture_output=True, text=True, timeout=900)
        (args.output / f"{name}.json").write_text(result.stdout)
        report = json.loads(result.stdout)
        print(f"{name}: exit={result.returncode} status={report['status']}", flush=True)
        # Preserve legitimate source findings, including Express's root image.
        assert result.returncode in [0, 1], report
        for experiment in ["container-build", "deployment-readiness"]:
            assert any(item["experiment_id"] == experiment and item["status"] == "pass" for item in report["evidence"]), report
        assert not any(item["status"] == "error" for item in report["evidence"]), report
        failures = [item for item in report["evidence"] if item["status"] == "fail"]
        for failure in failures:
            assert failure["experiment_id"] in ["graceful-shutdown", "pod-recovery", "rolling-deployment"], failure
            measurements = {item["name"]: item["value"] for item in failure.get("measurements", [])}
            assert int(measurements.get("dropped_requests", measurements.get("failed_requests", "0"))) > 0, failure
            assert 200 <= int(measurements.get("final_http_status", "0")) < 300, failure
        for experiment in ["graceful-shutdown", "pod-recovery", "rolling-deployment", "load-profile"]:
            assert any(item["experiment_id"] == experiment and item["execution"]["executed"] for item in report["evidence"]), report
        assert any(item["experiment_id"] == "load-profile" and item["status"] == "pass" for item in report["evidence"]), report
        for item in report["evidence"]:
            if item["execution"]["mutation_attempted"]:
                assert item["recovery"]["status"] == "pass", item
        if failures:
            print(f"{name}: original traffic failures retained alongside later experiment evidence", flush=True)
        assert report["fingerprint"]["source_commit"] == revision
        assert report["fingerprint"]["image_id"]
        assert "cloudforge-" not in subprocess.check_output(["k3d", "cluster", "list", "--no-headers"], text=True)
