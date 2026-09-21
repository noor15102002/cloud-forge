#!/usr/bin/env python3
"""Prove topology attribution and continuation using identical public app code."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.binary = str(Path(args.binary).resolve())
args.output.mkdir(parents=True, exist_ok=True)
reference = Path("testdata/healthy-node")
code_files = ["Dockerfile", "server.js", "package.json", "package-lock.json"]
source_hashes = {name: hashlib.sha256((reference / name).read_bytes()).hexdigest() for name in code_files}
summary = []
for origin, replicas in [("source", 1), ("source", 2), ("generated", 1)]:
    name = f"{origin}-{replicas}"
    with tempfile.TemporaryDirectory(prefix="cf-reliability-") as temporary:
        app = Path(temporary) / "application"
        shutil.copytree(reference, app)
        manifest = app / "k8s/app.yaml"
        # Same server and Dockerfile bytes. Only the deployment/topology changes.
        # Disable HPA and control/load endpoints uniformly to isolate availability.
        deployment = manifest.read_text().split("---")[0]
        if origin == "generated":
            shutil.rmtree(app / "k8s")
        else:
            manifest.write_text(deployment.replace("replicas: 2", f"replicas: {replicas}").replace("readinessProbe:\n", "readinessProbe:\n            initialDelaySeconds: 8\n"))
        (app / "cloudforge.yaml").write_text("schema_version: v1alpha1\nruntime: {port: 8080}\nendpoints: {health: /health, readiness: /ready}\n")
        before = {name: hashlib.sha256((app / name).read_bytes()).hexdigest() for name in code_files}
        assert before == source_hashes, "Application behavior changed between topology cases"
        environment = dict(os.environ, TMPDIR=temporary)
        planned = subprocess.run([args.binary, "verify", str(app), "--plan", "--format", "json"], capture_output=True, text=True, check=True, env=environment)
        (args.output / f"{name}.plan.json").write_text(planned.stdout)
        result = subprocess.run([args.binary, "verify", str(app), "--format", "json"], capture_output=True, text=True, timeout=900, env=environment)
        path = args.output / f"{name}.json"
        path.write_text(result.stdout)
        (args.output / f"{name}.stderr.txt").write_text(result.stderr)
        report = json.loads(result.stdout)
        assert report["schema_version"] == "v1alpha8"
        assert report["producer"]["commit"] not in ["", "unknown"]
        assert all(check["status"] == "supported" for check in report["compatibility"]["checks"] if check["name"].startswith("kubectl-"))
        tools = {tool["name"]: tool["version"] for tool in report["fingerprint"]["tools"]}
        missing_versions = [tool for tool in ("kubectl", "kubernetes") if tools.get(tool) in (None, "", "unknown")]
        assert not missing_versions, (
            f"Topology comparison lacks observed versions for {', '.join(missing_versions)}; "
            f"native status={report['status']}, exit_code={result.returncode}. "
            f"Inspect the preserved native report at {path} for the prerequisite failure."
        )
        assert tools["kubectl"] == "1.35.5" and tools["kubernetes"] == "1.35.5+k3s1", tools
        evidence = {item["experiment_id"]: item for item in report["evidence"]}
        expected = "fail" if origin == "source" and replicas == 1 else "pass"
        if origin == "generated":
            expected = "fail" if any(evidence[key]["status"] == "fail" for key in ["graceful-shutdown", "pod-recovery"]) else "pass"
        assert result.returncode == (1 if expected == "fail" else 0), report
        for experiment in ["graceful-shutdown", "pod-recovery"]:
            item = evidence[experiment]
            if origin == "source":
                assert item["status"] == expected, item
            else:
                assert item["status"] in ["pass", "fail"], item
            assert item["execution"]["executed"] and item["execution"]["mutation_attempted"]
            assert item["topology"]["origin"] == origin and item["topology"]["replicas"] == replicas
            assert item["topology"]["availability_probe_connection_policy"] == "new_connection_per_probe"
            assert item["recovery"]["status"] == "pass", item
            assert all(check["status"] == "pass" for check in item["recovery"]["checks"])
        rollout = evidence["rolling-deployment"]
        assert rollout["execution"]["executed"] and rollout["status"] == "pass" and rollout["recovery"]["status"] == "pass", rollout
        if expected == "fail":
            assert report["status"] == "fail", "Restoration erased original failure"
            assert "only replica" in evidence["graceful-shutdown"]["summary"]
        # The published loader and serialization must accept the new report.
        rendered = subprocess.check_output([args.binary, "report", str(path), "--format", "json"], text=True)
        assert rendered == subprocess.check_output([args.binary, "report", str(path), "--format", "json"], text=True)
        markdown = subprocess.check_output([args.binary, "report", str(path), "--format", "markdown"], text=True)
        (args.output / f"{name}.md").write_text(markdown)
        assert {name: hashlib.sha256((app / name).read_bytes()).hexdigest() for name in code_files} == source_hashes
        assert not list(Path(temporary).glob("cloudforge-verify-*")), "Private runtime workspace leaked"
        summary.append({"case": name, "status": report["status"], "source_hashes": source_hashes, "shutdown": evidence["graceful-shutdown"]["status"], "recovery": evidence["pod-recovery"]["status"], "rollout": rollout["status"], "cleanup": True})
        print(f"{name}: {report['status']}; original evidence preserved; later rollout passed", flush=True)
    subprocess.run(["bash", "scripts/pilot-cleanup-check.sh"], check=True)
(args.output / "topology-comparison.json").write_text(json.dumps(summary, indent=2) + "\n")
