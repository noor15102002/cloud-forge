#!/usr/bin/env python3
"""Reject unusable inventories before asserting that no public run resources remain."""
import re
import subprocess
import sys


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


def main():
    inspections = {
        "container": ["docker", "container", "ls", "--all", "--no-trunc", "--filter", "name=cloudforge-", "--format", "{{.ID}} {{.Names}}"],
        "network": ["docker", "network", "ls", "--no-trunc", "--filter", "name=k3d-cloudforge-", "--format", "{{.ID}} {{.Name}}"],
        "volume": ["docker", "volume", "ls", "--format", "{{.Name}}"],
        "image": ["docker", "image", "ls", "--no-trunc", "--format", "{{.ID}} {{.Repository}}:{{.Tag}}"],
    }
    remnants = {}
    for kind, command in inspections.items():
        observed = subprocess.run(command, capture_output=True, text=True, timeout=12, check=True)
        names = records(observed.stdout, kind)
        matches = [name for name in names if (kind == "container" and "cloudforge-" in name)
                   or (kind == "network" and name.startswith("k3d-cloudforge-"))
                   or (kind == "volume" and name.startswith(("k3d-cloudforge-", "buildx_buildkit_cloudforge-")))
                   or (kind == "image" and name.startswith("cloudforge/"))]
        if matches:
            remnants[kind] = matches
    if remnants:
        print("Possible CloudForge remnants remain; independent cleanup observation failed.", file=sys.stderr)
        for kind, names in remnants.items():
            print(f"{kind}: {len(names)}", file=sys.stderr)
        return 1
    print("Independent complete Docker inventories contain no CloudForge resource names.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"Cleanup absence could not be established: {type(error).__name__}", file=sys.stderr)
        sys.exit(2)
