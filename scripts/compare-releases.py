#!/usr/bin/env python3
"""Compare release bytes and deterministic metadata, retaining both build clocks."""
import argparse
import hashlib
import json
from pathlib import Path
import tarfile

from release_metadata import deterministic_manifest


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate release metadata key")
        result[key] = value
    return result


def inspect_release(directory):
    manifest = json.loads((directory / "release.json").read_text(), object_pairs_hook=unique_object)
    deterministic_manifest(manifest)
    archive_name = f"cloudforge_{manifest['version']}_linux_amd64.tar.gz"
    if manifest["archive"] != archive_name or Path(archive_name).name != archive_name:
        raise ValueError("unexpected release archive name")
    archive = directory / archive_name
    archive_hash = hashlib.sha256(archive.read_bytes()).hexdigest()
    if archive_hash != manifest["archive_sha256"]:
        raise ValueError("release archive checksum mismatch")
    checksums = (directory / "checksums.txt").read_bytes()
    if checksums != f"{archive_hash}  {archive_name}\n".encode():
        raise ValueError("release checksum record mismatch")
    with tarfile.open(archive, "r:gz") as package:
        members = package.getmembers()
        if len(members) != 2 or {item.name for item in members} != {"cloudforge", "LICENSE"} or not all(item.isfile() and 0 < item.size <= 64 * 1024 * 1024 for item in members):
            raise ValueError("unexpected release archive members")
        binary_hash = hashlib.sha256(package.extractfile("cloudforge").read()).hexdigest()
    if binary_hash != manifest["binary_sha256"]:
        raise ValueError("release binary checksum mismatch")
    return manifest


def compare(first, second):
    left, right = inspect_release(first), inspect_release(second)
    if deterministic_manifest(left) != deterministic_manifest(right):
        raise ValueError("reproduced bytes or deterministic release metadata differ")
    return {"result": "pass", "proof_type": "reproducible-build comparison",
            "binary_sha256": left["binary_sha256"], "archive_sha256": left["archive_sha256"],
            "excluded_fields": ["qualification_build_started_at", "qualification_build_finished_at"],
            "canonical_manifest": left, "reproduced_manifest": right}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("canonical", type=Path)
    parser.add_argument("reproduced", type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(compare(args.canonical, args.reproduced), indent=2, sort_keys=True))
    except (OSError, ValueError, KeyError, tarfile.TarError) as error:
        parser.exit(2, f"Release reproducibility check failed: {error}\n")


if __name__ == "__main__":
    main()
