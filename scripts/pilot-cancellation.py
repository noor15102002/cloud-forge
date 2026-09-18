#!/usr/bin/env python3
"""Interrupt real tool execution on a disposable runner and check owned cleanup."""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser()
parser.add_argument("binary")
parser.add_argument("output", type=Path)
args = parser.parse_args()
args.binary = str(Path(args.binary).resolve())
args.output.mkdir(parents=True, exist_ok=True)
original_kubeconfig = Path.home() / ".kube" / "config"
original_bytes = original_kubeconfig.read_bytes() if original_kubeconfig.exists() else None

wrapper = '''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
tool = pathlib.Path(sys.argv[0]).name
arguments = sys.argv[1:]
stage = os.environ['CLOUDFORGE_PILOT_STAGE']
selected = (stage == 'build' and tool == 'docker' and 'build' in arguments) or (stage == 'cluster' and tool == 'k3d' and 'create' in arguments) or (stage == 'readiness' and tool == 'kubectl' and 'rollout' in arguments and 'status' in arguments) or (stage == 'load' and tool == 'k6' and 'run' in arguments)
process = subprocess.Popen([os.environ['CLOUDFORGE_REAL_' + tool.upper()], *arguments])
if selected:
    pathlib.Path(os.environ['CLOUDFORGE_PILOT_MARKER']).write_text(json.dumps({'tool':tool,'pid':process.pid}))
sys.exit(process.wait())
'''

@contextmanager
def existing_state():
    """Use recognizable pre-existing state to prove cleanup ownership boundaries."""
    name = "pilot-existing-" + str(os.getpid())
    created = []
    try:
        subprocess.run(["docker", "network", "create", name], check=True, capture_output=True, timeout=30)
        created.append("network")
        subprocess.run(["docker", "volume", "create", name], check=True, capture_output=True, timeout=30)
        created.append("volume")
        subprocess.run(["docker", "run", "--detach", "--name", name, "--network", name, "--volume", name + ":/data", "alpine:3.22", "sleep", "3600"], check=True, capture_output=True, timeout=120)
        created.append("container")
        original_kubeconfig.parent.mkdir(parents=True, exist_ok=True)
        original_kubeconfig.write_text("apiVersion: v1\nkind: Config\ncurrent-context: existing\nclusters:\n- name: existing\n  cluster:\n    server: https://127.0.0.1:1\ncontexts:\n- name: existing\n  context:\n    cluster: existing\n    user: existing\nusers:\n- name: existing\n  user: {}\n")
        original_kubeconfig.chmod(0o600)
        baseline = original_kubeconfig.read_bytes()
        identities = {kind: subprocess.check_output(["docker", kind, "inspect", "--format", "{{.Id}}" if kind != "volume" else "{{.Name}}", name], text=True).strip() for kind in created}
        yield name, baseline, identities
    finally:
        for kind in reversed(created):
            arguments = ["docker", kind, "rm"]
            if kind == "container":
                arguments.append("--force")
            subprocess.run([*arguments, name], capture_output=True, timeout=30)
        if original_bytes is None:
            original_kubeconfig.unlink(missing_ok=True)
        else:
            original_kubeconfig.write_bytes(original_bytes)


with existing_state() as (sentinel_name, baseline_kubeconfig, sentinel_ids):
    for stage in ["build", "cluster", "readiness", "load"]:
        with tempfile.TemporaryDirectory(prefix="cloudforge-cancel-") as temporary:
            root = Path(temporary)
            app = root / "app"
            shutil.copytree("testdata/healthy-node", app)
            # Keep each targeted phase long enough to establish an actual interruption.
            if stage == "build":
                dockerfile = app / "Dockerfile"
                dockerfile.write_text(dockerfile.read_text().replace("WORKDIR /app", "RUN sleep 60\nWORKDIR /app"))
            if stage == "readiness":
                server = app / "server.js"
                server.write_text(server.read_text().replace("let ready = true", "let ready = false"))
            config = app / "cloudforge.yaml"
            config.write_text(config.read_text().split("experiments:")[0])
            bin_dir = root / "bin"
            bin_dir.mkdir()
            environment = dict(os.environ)
            for tool in ["docker", "k3d", "kubectl", "k6"]:
                real = shutil.which(tool)
                assert real, tool
                environment["CLOUDFORGE_REAL_" + tool.upper()] = real
                file = bin_dir / tool
                file.write_text(wrapper)
                file.chmod(0o700)
            marker = root / "started.json"
            environment.update(PATH=str(bin_dir) + os.pathsep + os.environ["PATH"], CLOUDFORGE_PILOT_STAGE=stage, CLOUDFORGE_PILOT_MARKER=str(marker))
            report_path = args.output / f"cancel-{stage}.json"
            with report_path.open("w") as stdout, (args.output / f"cancel-{stage}.stderr").open("w") as stderr:
                process = subprocess.Popen([args.binary, "verify", str(app), "--format", "json"], stdout=stdout, stderr=stderr, env=environment)
                deadline = time.monotonic() + 600
                while not marker.exists() and process.poll() is None and time.monotonic() < deadline:
                    time.sleep(0.1)
                if not marker.exists():
                    process.send_signal(signal.SIGINT)
                    process.wait(timeout=180)
                    raise AssertionError(f"{stage}: target phase was not reached")
                time.sleep(2)
                assert process.poll() is None, f"{stage}: verification ended before interruption"
                process.send_signal(signal.SIGINT)
                assert process.wait(timeout=180) == 2, f"{stage}: cancellation was not classified as execution error"
            report = json.loads(report_path.read_text())
            assert report["status"] == "error", report
            subprocess.run(["bash", "scripts/pilot-cleanup-check.sh"], check=True)
            current_bytes = original_kubeconfig.read_bytes() if original_kubeconfig.exists() else None
            assert current_bytes == baseline_kubeconfig, "User kubeconfig changed"
            for kind, expected in sentinel_ids.items():
                actual = subprocess.check_output(["docker", kind, "inspect", "--format", "{{.Id}}" if kind != "volume" else "{{.Name}}", sentinel_name], text=True).strip()
                assert actual == expected, f"Unrelated {kind} changed"
            running = subprocess.check_output(["docker", "container", "inspect", "--format", "{{.State.Running}}", sentinel_name], text=True).strip()
            assert running == "true", "Unrelated container was stopped"
            print(f"{stage}: interrupted real subprocess; owned resources removed; kubeconfig unchanged", flush=True)
