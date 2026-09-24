#!/usr/bin/env bash
# Build a reproducible candidate from one clean, committed source revision.
set -euo pipefail

if [ "$#" -ne 2 ] || [[ ! "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo 'usage: build-release.sh <vMAJOR.MINOR.PATCH[-prerelease]> <output-directory>' >&2
  exit 2
fi
release_version="$1"
release_output="$(realpath -m "$2")"
if [ -e "$release_output" ] && { [ ! -d "$release_output" ] || [ -n "$(find "$release_output" -mindepth 1 -maxdepth 1 -print -quit)" ]; }; then
  echo 'Release output must be a new or empty directory; preserve the previous attempt.' >&2
  exit 2
fi
cd "$(dirname "$0")/.."
test -z "$(git status --porcelain --untracked-files=all)" || {
  echo 'Release candidates require a clean committed checkout.' >&2
  exit 2
}
release_commit="$(git rev-parse HEAD)"
release_epoch="$(git show -s --format=%ct HEAD)"
release_date="$(date -u --date="@$release_epoch" '+%Y-%m-%dT%H:%M:%SZ')"
release_toolchain="$(awk '$1 == "toolchain" {print $2}' go.mod)"
test "$release_toolchain" = go1.27.1
export GOTOOLCHAIN="$release_toolchain" CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOWORK=off GOFLAGS= GOEXPERIMENT=
test "$(go version | awk '{print $3}')" = "$release_toolchain"
release_temporary="$(mktemp -d)"
trap 'rm -rf "$release_temporary"' EXIT
release_started_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
release_package=github.com/noor15102002/cloud-forge/internal/cli
go build -mod=readonly -trimpath -buildvcs=false \
  -ldflags "-s -w -buildid= -X $release_package.Version=$release_version -X $release_package.Commit=$release_commit -X $release_package.Date=$release_date" \
  -o "$release_temporary/cloudforge" ./cmd/cloudforge
python3 - "$release_temporary/cloudforge" "$release_output" "$release_version" "$release_commit" "$release_date" "$release_epoch" "$release_toolchain" "$release_started_at" <<'PY'
import gzip, hashlib, json, pathlib, sys, tarfile
from datetime import datetime, timezone
from scripts.release_metadata import enrich_manifest
binary, output, version, commit, date, epoch, toolchain, started_at = sys.argv[1:]
output = pathlib.Path(output)
output.mkdir(parents=True, exist_ok=True)
archive = output / f"cloudforge_{version}_linux_amd64.tar.gz"
with archive.open("xb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=int(epoch)) as compressed:
    with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as package:
        for source, name, mode in ((pathlib.Path(binary), "cloudforge", 0o755), (pathlib.Path("LICENSE"), "LICENSE", 0o644)):
            info = tarfile.TarInfo(name)
            info.size = source.stat().st_size
            info.mode, info.mtime, info.uid, info.gid = mode, int(epoch), 0, 0
            with source.open("rb") as stream:
                package.addfile(info, stream)
digest = lambda path: hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
manifest = {"version": version, "commit": commit, "date": date, "source_date_epoch": int(epoch),
            "go_version": toolchain, "os": "linux", "arch": "amd64", "archive": archive.name,
            "archive_sha256": digest(archive), "binary_sha256": digest(binary)}
manifest = enrich_manifest(manifest, started_at, datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
(output / "release.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
(output / "checksums.txt").write_text(f"{manifest['archive_sha256']}  {archive.name}\n")
print(json.dumps(manifest, indent=2, sort_keys=True))
PY
