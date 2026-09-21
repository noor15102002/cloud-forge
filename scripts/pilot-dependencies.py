#!/usr/bin/env python3
"""Validate generic Redis-backed applications on a disposable runner only."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("fixture", choices=["healthy-node-redis", "healthy-python-redis"])
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.binary = str(Path(args.binary).resolve())
args.output.mkdir(parents=True, exist_ok=True)


def run_case(name, config=None, env=None):
    command = [args.binary, "verify", f"testdata/{args.fixture}", "--format", "json"]
    if config:
        command += ["--config", config]
    with tempfile.TemporaryDirectory(prefix="cf-dependency-private-") as private:
        environment = dict(os.environ if env is None else env, TMPDIR=private)
        # Retain bounded image-import diagnostics for these public fixtures only.
        # Never capture kubeconfig retrieval or application/container logs.
        wrapper = Path(private) / "k3d"
        wrapper.write_text('''#!/usr/bin/env python3
import os, pathlib, subprocess, sys
args = sys.argv[1:]
real = os.environ['CF_REAL_IMPORT_K3D']
if args[:2] != ['image', 'import']:
    os.execv(real, [real, *args])
result = subprocess.run([real, *args], capture_output=True)
with pathlib.Path(os.environ['CF_IMPORT_LOG']).open('ab') as log:
    log.write((result.stdout + result.stderr)[-65536:])
sys.stdout.buffer.write(result.stdout)
sys.stderr.buffer.write(result.stderr)
sys.exit(result.returncode)
''')
        wrapper.chmod(0o700)
        environment.update(CF_REAL_IMPORT_K3D=shutil.which("k3d"), CF_IMPORT_LOG=str((args.output / f"{name}.image-import.txt").resolve()))
        environment["PATH"] = private + os.pathsep + environment["PATH"]
        result = subprocess.run(command, capture_output=True, text=True, timeout=1000, env=environment)
        assert not list(Path(private).glob("cloudforge-verify-*")), "Temporary kubeconfig/runtime directory leaked"
    path = args.output / f"{name}.json"
    path.write_text(result.stdout)
    (args.output / f"{name}.stderr.txt").write_text(result.stderr)
    report = json.loads(result.stdout)
    assert report["schema_version"] == "v1alpha7"
    # Exercise the same strict schema loader used by users and the Action.
    rendered = subprocess.run([args.binary, "report", str(path), "--format", "json"], capture_output=True, text=True, check=True)
    repeat = subprocess.run([args.binary, "report", str(path), "--format", "json"], capture_output=True, text=True, check=True)
    assert rendered.stdout == repeat.stdout
    subprocess.run(["bash", "scripts/pilot-cleanup-check.sh"], check=True)
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    print(f"{name}: exit={result.returncode}; status={report['status']}; redis={report['dependencies'][0]['status']}", flush=True)
    return result, report, evidence


result, report, evidence = run_case("healthy")
assert result.returncode == 0, report.get("diagnostics")
for experiment in ["dependency.redis", "container-build", "deployment-readiness", "semantic-readiness", "readiness-gating", "inflight-shutdown", "pod-recovery", "rolling-deployment", "load-profile"]:
    assert evidence[experiment]["status"] == "pass", evidence[experiment]
assert report["dependencies"][0]["network_exposure"] == "cluster-internal"
assert "@sha256:" in report["dependencies"][0]["image"]
assert report["fingerprint"]["compatibility_key"]
assert report["fingerprint"]["dependencies"][0]["digest"].startswith("sha256:")
assert evidence["dependency-loss"]["status"] == "skipped"

for name in ["semantic-degraded", "disconnected"]:
    result, report, evidence = run_case(name, f"testdata/redis-failures/{name}.yaml")
    assert result.returncode == 1 and report["status"] == "fail"
    assert evidence["dependency.redis"]["status"] == "pass"
    assert evidence["deployment-readiness"]["status"] == "fail"
    assert evidence["semantic-readiness"]["status"] == "fail"
    if name == "semantic-degraded":
        measurements = {item["name"]: item["value"] for item in evidence["semantic-readiness"]["measurements"]}
        assert measurements["http_status"] == "200"
        assert measurements["json_status_matched"] == "false"

# Deterministic infrastructure fault injection belongs to this disposable harness,
# not to the product config: delay Redis readiness beyond its 1s startup budget.
with tempfile.TemporaryDirectory(prefix="cf-redis-fault-") as temporary:
    wrapper = Path(temporary) / "kubectl"
    real = shutil.which("kubectl")
    wrapper.write_text('''#!/usr/bin/env python3
import os, pathlib, re, sys
args = sys.argv[1:]
if 'apply' in args and args[-1].endswith('dependency-redis.yaml'):
    path = pathlib.Path(args[-1])
    value = path.read_text()
    value = re.sub(r'(?m)^(\\s+)periodSeconds: 1$', r'\\1initialDelaySeconds: 60\\n\\1periodSeconds: 1', value)
    path.write_text(value)
os.execv(os.environ['CF_REAL_KUBECTL'], [os.environ['CF_REAL_KUBECTL'], *args])
''')
    wrapper.chmod(0o700)
    environment = dict(os.environ, PATH=temporary + os.pathsep + os.environ["PATH"], CF_REAL_KUBECTL=real)
    result, report, evidence = run_case("dependency-timeout", "testdata/redis-failures/startup-timeout.yaml", environment)
    assert result.returncode == 1 and report["status"] == "blocked"
    assert evidence["dependency.redis"]["status"] == "fail"
    assert evidence["deployment-readiness"]["status"] == "blocked"
    assert not any(item["id"] == "container.startup" for item in report["findings"])
