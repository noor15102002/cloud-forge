#!/usr/bin/env python3
"""Install a checksum-verified Linux/amd64 candidate without rebuilding it."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import stat
import subprocess
import tarfile
import tempfile


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate release metadata key")
        value[key] = item
    return value


def read_regular(path, limit):
    if path.is_symlink() or not stat.S_ISREG(path.stat().st_mode) or path.stat().st_size > limit:
        raise ValueError("release input must be a bounded regular file")
    return path.read_bytes()


def install(source, destination, version, commit, date, archive_sha256):
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
        raise ValueError("the qualified candidate requires Linux/amd64")
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("expected release version is required")
    if not re.fullmatch(r"[a-f0-9]{40}", commit):
        raise ValueError("expected full source commit is required")
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", date):
        raise ValueError("expected UTC build date is required")
    if not re.fullmatch(r"[a-f0-9]{64}", archive_sha256):
        raise ValueError("expected archive SHA-256 is required")
    manifest = json.loads(read_regular(source / "release.json", 16384), object_pairs_hook=unique_object)
    fields = {"version", "commit", "date", "source_date_epoch", "go_version", "os", "arch", "archive", "archive_sha256", "binary_sha256"}
    if not isinstance(manifest, dict) or set(manifest) != fields:
        raise ValueError("unsupported release manifest")
    expected = {"version": version, "commit": commit, "date": date, "os": "linux", "arch": "amd64",
                "archive": f"cloudforge_{version}_linux_amd64.tar.gz", "archive_sha256": archive_sha256}
    if any(manifest.get(key) != value for key, value in expected.items()):
        raise ValueError("release manifest identity mismatch")
    if manifest["go_version"] != "go1.27.1" or type(manifest["source_date_epoch"]) is not int:
        raise ValueError("unsupported release build metadata")
    if datetime.fromtimestamp(manifest["source_date_epoch"], timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") != date:
        raise ValueError("release date and source epoch disagree")
    if not re.fullmatch(r"[a-f0-9]{64}", str(manifest["binary_sha256"])):
        raise ValueError("invalid binary checksum")
    archive = source / manifest["archive"]
    digest = hashlib.sha256(read_regular(archive, 128 * 1024 * 1024)).hexdigest()
    if digest != archive_sha256:
        raise ValueError("candidate archive checksum mismatch")
    if read_regular(source / "checksums.txt", 1024).decode() != f"{digest}  {archive.name}\n":
        raise ValueError("published checksum list mismatch")
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".cloudforge-install-", dir=destination) as temporary:
        binary = Path(temporary) / "cloudforge"
        with tarfile.open(archive, mode="r:gz") as package:
            members = package.getmembers()
            if {member.name for member in members} != {"cloudforge", "LICENSE"} or len(members) != 2:
                raise ValueError("unexpected candidate archive members")
            for member in members:
                if not member.isfile() or member.size < 1 or member.size > 64 * 1024 * 1024:
                    raise ValueError("unsafe candidate archive member")
            binary.write_bytes(package.extractfile("cloudforge").read())
        if hashlib.sha256(binary.read_bytes()).hexdigest() != manifest["binary_sha256"]:
            raise ValueError("candidate binary checksum mismatch")
        binary.chmod(0o755)
        result = subprocess.run([str(binary), "version", "--format", "json"], capture_output=True, check=True, timeout=15)
        identity = json.loads(result.stdout, object_pairs_hook=unique_object)
        if identity != {"schema_version": "v1alpha1", "version": version, "commit": commit, "date": date}:
            raise ValueError("candidate binary identity mismatch")
        os.replace(binary, destination / "cloudforge")
    (destination / "installation.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    return destination / "cloudforge"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--date", required=True)
    parser.add_argument("--sha256", required=True)
    args = parser.parse_args()
    try:
        print(install(args.source.resolve(), args.destination.resolve(), args.version, args.commit, args.date, args.sha256))
    except (OSError, ValueError, OverflowError, KeyError, tarfile.TarError, subprocess.SubprocessError) as error:
        parser.exit(2, f"Candidate installation failed: {error}\n")


if __name__ == "__main__":
    main()
