#!/usr/bin/env python3
"""Run one generic public qualification case with an already installed candidate."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("case")
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    args.binary = args.binary.resolve()
    # Importing the existing public helper does not touch Docker or private applications.
    import importlib.util
    spec = importlib.util.spec_from_file_location("public_guards", Path(__file__).with_name("pilot-backend-cancellation.py"))
    guards = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(guards)
    guards.require_runner()
    args.output.mkdir(parents=True, exist_ok=False)
    manifest = json.loads((args.binary.parent / "installation.json").read_text())
    if hashlib.sha256(args.binary.read_bytes()).hexdigest() != manifest["binary_sha256"]:
        raise ValueError("installed candidate changed")
    (args.output / "candidate.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    case_output = args.output / "native"
    base = [sys.executable]
    commands = {
        "healthy-node": base + ["scripts/pilot-fixtures.py", str(args.binary), "healthy-node", str(case_output)],
        "healthy-python": base + ["scripts/pilot-fixtures.py", str(args.binary), "healthy-python", str(case_output)],
        "redis-node": base + ["scripts/pilot-dependencies.py", str(args.binary), "healthy-node-redis", str(case_output)],
        "redis-python": base + ["scripts/pilot-dependencies.py", str(args.binary), "healthy-python-redis", str(case_output)],
        "external": base + ["scripts/pilot-external.py", str(args.binary), str(case_output)],
        "reliability": base + ["scripts/pilot-reliability.py", str(args.binary), str(case_output)],
        "topology": base + ["scripts/pilot-topology.py", str(args.binary), str(case_output)],
        "probe-pacing": base + ["scripts/pilot-probe-pacing.py", str(args.binary), str(case_output)],
    }
    if args.case.startswith("operational-"):
        case = args.case.removeprefix("operational-")
        command = base + ["scripts/pilot-operational.py", str(args.binary), str(case_output), "--case", case]
    elif args.case.startswith("cancel-") and args.case != "cancel-redis-fallback":
        stage = args.case.removeprefix("cancel-")
        if stage not in ("build", "cluster", "redis", "readiness", "lifecycle", "load"):
            raise ValueError("unknown public cancellation case")
        command = base + ["scripts/pilot-cancellation.py", str(args.binary), str(case_output), "--stages", stage]
    elif args.case == "cancel-redis-fallback":
        command = base + ["scripts/pilot-cancellation.py", str(args.binary), str(case_output), "--stages", "redis", "--force-builder-cleanup-failure"]
    elif args.case.startswith("backend-") and not args.case.startswith("backend-cancel-"):
        case = args.case.removeprefix("backend-")
        if case not in ("healthy", "preparation-failure", "missing-key", "missing-schema"):
            raise ValueError("unknown public backend case")
        command = base + ["scripts/pilot-backend.py", str(args.binary), str(case_output), "--run", "--case", case]
    elif args.case.startswith("worker-"):
        case = args.case.removeprefix("worker-")
        if case not in ("healthy", "never", "stale", "frozen", "future", "malformed", "exit", "first-pod-only", "fail-second-start", "cancel-startup", "cancel-recovery"):
            raise ValueError("unknown public worker case")
        command = base + ["scripts/pilot-worker.py", str(args.binary), str(case_output), "--run", "--case", case]
    elif args.case.startswith("backend-cancel-"):
        stage = args.case.removeprefix("backend-cancel-")
        if stage not in ("clamav", "postgresql", "preparation"):
            raise ValueError("unknown public backend cancellation stage")
        command = base + ["scripts/pilot-backend-cancellation.py", str(args.binary), str(case_output), "--run", "--stage", stage]
    else:
        command = commands[args.case]
    result = None
    try:
        with (args.output / "harness.stdout.txt").open("x") as stdout, (args.output / "harness.stderr.txt").open("x") as stderr:
            result = subprocess.run(command, stdout=stdout, stderr=stderr, check=False)
    finally:
        (args.output / "harness.exit.json").write_text(json.dumps({"exit_code": result.returncode if result else None}) + "\n")
    identities = []
    for report_path in sorted(case_output.rglob("*.json")):
        try:
            report = json.loads(report_path.read_text())
        except (ValueError, OSError):
            continue  # Raw unusable outputs remain evidence and the harness rejects them.
        if isinstance(report, dict) and "producer" in report:
            expected = {"version": manifest["version"], "commit": manifest["commit"]}
            if report["producer"] != expected:
                raise ValueError("runtime report producer differs from the installed candidate")
            identities.append(str(report_path.relative_to(args.output)))
    (args.output / "producer-check.json").write_text(json.dumps({"reports": identities, "candidate_sha256": manifest["binary_sha256"]}, indent=2) + "\n")
    if result.returncode:
        sys.stderr.write((args.output / "harness.stderr.txt").read_text())
        return result.returncode
    if not identities:
        raise ValueError("no candidate runtime producer evidence was observed")
    print(f"{args.case}: qualified {len(identities)} reports with the installed candidate")
    return 0


if __name__ == "__main__":
    sys.exit(main())
