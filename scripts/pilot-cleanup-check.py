#!/usr/bin/env python3
"""Reject unusable inventories before asserting that no public run resources remain."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


def records(payload, kind):
    if len(payload) > 1024 * 1024 or (payload and not payload.endswith("\n")):
        raise ValueError("cleanup inventory is incomplete")
    found, seen = [], set()
    for line in payload.splitlines():
        parts = line.split()
        expected = 1 if kind == "volume" else 2
        if len(parts) != expected or tuple(parts) in seen:
            raise ValueError("cleanup inventory has invalid or duplicate records")
        seen.add(tuple(parts))
        if kind != "volume" and not re.fullmatch(r"(?:sha256:)?[a-f0-9]{64}", parts[0]):
            raise ValueError("cleanup inventory has an incomplete resource identity")
        name = parts[-1]
        if kind == "image":
            if not re.fullmatch(r"[A-Za-z0-9_./:<>-]+", name):
                raise ValueError("invalid image inventory record")
        elif not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", name):
            raise ValueError("invalid cleanup inventory name")
        found.append(name)
    return found


def builder_records(payload):
    """Buildx JSON emits one builder per line, with node names nested inside it."""
    if len(payload) > 1024 * 1024 or (payload and not payload.endswith("\n")):
        raise ValueError("builder inventory is incomplete")

    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate builder JSON key")
            value[key] = item
        return value

    def reject_constant(_):
        raise ValueError("nonfinite builder JSON value")

    names = []
    for line in payload.splitlines():
        value = json.loads(line, object_pairs_hook=unique, parse_constant=reject_constant)
        if not isinstance(value, dict) or not isinstance(value.get("Name"), str):
            raise ValueError("invalid builder inventory")
        name = value["Name"]
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", name) or name in names:
            raise ValueError("invalid or duplicate builder identity")
        if value.get("Err") or not isinstance(value.get("Nodes"), list):
            raise ValueError("unusable builder observation")
        if any(not isinstance(node, dict) or node.get("Err") for node in value["Nodes"]):
            raise ValueError("unusable builder node observation")
        names.append(name)
    return names


def timestamp():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def write_record(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    # Never replace evidence from an earlier invocation.
    with path.open("x", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")


def private_state():
    """Observe known workspace roots and kubeconfig without retaining contents."""
    kubeconfig = Path.home() / ".kube" / "config"
    if kubeconfig.is_symlink():
        raise ValueError("kubeconfig_symlink")
    kube = {"exists": kubeconfig.exists()}
    if kube["exists"]:
        if not kubeconfig.is_file() or kubeconfig.stat().st_size > 1024 * 1024:
            raise ValueError("kubeconfig_unusable")
        kube.update(sha256=hashlib.sha256(kubeconfig.read_bytes()).hexdigest(),
                    mode=kubeconfig.stat().st_mode & 0o777)
    roots = {"temporary": Path(tempfile.gettempdir())}
    if os.environ.get("RUNNER_TEMP"):
        roots["runner_temporary"] = Path(os.environ["RUNNER_TEMP"])
    workspaces = {}
    for kind, root in roots.items():
        if root.is_symlink() or not root.is_dir():
            raise ValueError("workspace_root_unusable")
        # Nested private roots are checked by their individual harnesses before
        # removal. This independent snapshot claims only these direct roots.
        names = sorted(entry.name for entry in root.glob("cloudforge-verify-*"))
        if len(names) > 1024:
            raise ValueError("workspace_inventory_exceeded_bound")
        workspaces[kind] = names
    return {"kubeconfig": kube, "direct_runtime_workspaces": workspaces}


def observe_resources(runner=subprocess.run):
    inspections = {
        "container": ["docker", "container", "ls", "--all", "--no-trunc", "--filter", "name=cloudforge-", "--format", "{{.ID}} {{.Names}}"],
        "network": ["docker", "network", "ls", "--no-trunc", "--filter", "name=k3d-cloudforge-", "--format", "{{.ID}} {{.Name}}"],
        "volume": ["docker", "volume", "ls", "--format", "{{.Name}}"],
        "image": ["docker", "image", "ls", "--no-trunc", "--format", "{{.ID}} {{.Repository}}:{{.Tag}}"],
        # Include dangling/intermediate images: an ownership label is queried
        # by key only, so private marker values never enter retained output.
        "owned_image": ["docker", "image", "ls", "--all", "--no-trunc", "--filter", "label=cloudforge.dev/ownership", "--format", "{{.ID}} {{.Repository}}:{{.Tag}}"],
        "builder": ["docker", "buildx", "ls", "--format", "json"],
    }
    def observe(item):
        kind, command = item
        try:
            observed = runner(command, capture_output=True, text=True, timeout=12, check=True)
            shape = "image" if kind == "owned_image" else kind
            names = builder_records(observed.stdout) if kind == "builder" else records(observed.stdout, shape)
            matches = [name for name in names if (kind == "container" and "cloudforge-" in name)
                       or (kind == "network" and name.startswith("k3d-cloudforge-"))
                       or (kind == "volume" and name.startswith(("k3d-cloudforge-", "buildx_buildkit_cloudforge-")))
                       or (kind == "image" and name.startswith("cloudforge/"))
                       or kind == "owned_image" or (kind == "builder" and name.startswith("cloudforge-"))]
            wanted = set(matches)
            identities = ([{"id": name, "name": name} for name in matches] if kind == "builder" else
                          [{"id": line.split()[0], "name": line.split()[-1]}
                           for line in observed.stdout.splitlines() if line.split()[-1] in wanted])
            return kind, {"status": "ERROR" if matches else "PASS", "completion_observed": True,
                          "possible_remnants": len(matches), "observed_identities": identities[:128],
                          "identities_omitted": max(0, len(identities) - 128),
                          "reason": "possible_run_remnants" if matches else "complete_inventory_no_matches"}
        except (ValueError, RecursionError, OSError, subprocess.SubprocessError):
            # Inspect all remaining classes even if one observation fails.
            return kind, {"status": "UNKNOWN", "completion_observed": False,
                          "possible_remnants": None, "reason": "inventory_unavailable_or_unusable"}
    # Independent read-only inventories share the same bounded observation
    # window; one blocked daemon query must not consume every later query's time.
    with ThreadPoolExecutor(max_workers=len(inspections)) as pool:
        return dict(pool.map(observe, inspections.items()))


def qualify_cleanup(classes, baseline=None, state_observer=private_state):
    reasons = []
    if baseline is not None:
        try:
            expected = json.loads(Path(baseline).read_text())
            if expected.get("schema_version") != "qualification-cleanup-baseline-v1" or expected.get("status") != "PASS":
                raise ValueError("invalid_baseline")
            current = state_observer()
            for kind in ("kubeconfig", "direct_runtime_workspaces"):
                equal = expected["state"][kind] == current[kind]
                classes[kind] = {"status": "PASS" if equal else "ERROR", "completion_observed": True,
                                 "reason": "baseline_unchanged" if equal else "baseline_changed"}
        except (ValueError, KeyError, TypeError, OSError):
            classes["private_state"] = {"status": "UNKNOWN", "completion_observed": False,
                                        "reason": "private_state_or_baseline_unavailable"}
    else:
        reasons.append("private_state_baseline_not_supplied")
    statuses = {item["status"] for item in classes.values()}
    # Retain positive remnants even when another class is unobservable.
    status = "ERROR" if "ERROR" in statuses else "UNKNOWN" if "UNKNOWN" in statuses else "PASS"
    reasons.extend(sorted({item["reason"] for item in classes.values() if item["status"] != "PASS"}))
    return status, reasons


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--capture-baseline", type=Path)
    args = parser.parse_args(argv)
    started = timestamp()
    if args.capture_baseline:
        try:
            value = {"schema_version": "qualification-cleanup-baseline-v1", "status": "PASS", "state": private_state()}
        except (ValueError, OSError):
            value = {"schema_version": "qualification-cleanup-baseline-v1", "status": "UNKNOWN", "reason": "baseline_unavailable"}
        value.update(started_at=started, finished_at=timestamp())
        write_record(args.capture_baseline, value)
        return 0 if value["status"] == "PASS" else 2
    classes = observe_resources()
    status, reasons = qualify_cleanup(classes, args.baseline)
    code = 0 if status == "PASS" else 1 if status == "ERROR" else 2
    record = {"schema_version": "qualification-cleanup-v1", "status": status,
              "completion_observed": all(item["completion_observed"] for item in classes.values()),
              "exit_code": code, "started_at": started, "finished_at": timestamp(),
              "reason_codes": reasons, "resource_classes": classes,
              "baseline_checked": args.baseline is not None and "private_state" not in classes,
              "scope": "read_only_possible_run_resources_no_deletion",
              "limitations": ["Private workspace comparison covers the direct temporary roots; nested harness roots need their retained per-case checks."]}
    if args.output:
        write_record(args.output, record)
    print(json.dumps(record, sort_keys=True))
    return code


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"Cleanup absence could not be established: {type(error).__name__}", file=sys.stderr)
        sys.exit(2)
