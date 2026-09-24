#!/usr/bin/env bash
# Both qualification and released Action invocations install the packaged bytes.
set -euo pipefail
action_root="$(cd "$(dirname "$0")/.." && pwd)"
install_dir="$(mktemp -d "$RUNNER_TEMP/cloudforge-action.XXXXXX")"
candidate_path="${CLOUDFORGE_CANDIDATE_PATH:-}"
if [ -n "$candidate_path" ]; then
  python3 "$action_root/scripts/install-candidate.py" "$candidate_path" "$install_dir" \
    --version "$CLOUDFORGE_CANDIDATE_VERSION" --commit "$CLOUDFORGE_CANDIDATE_COMMIT" \
    --date "$CLOUDFORGE_CANDIDATE_DATE" --sha256 "$CLOUDFORGE_CANDIDATE_SHA256" --require-enriched-manifest
  actual_commit="$(git -C "$action_root" rev-parse HEAD)"
  test "$actual_commit" = "$CLOUDFORGE_CANDIDATE_COMMIT" || {
    echo 'Candidate and packaged Action source revisions differ.' >&2; exit 2;
  }
elif [ -n "${CLOUDFORGE_ACTION_REF:-}" ]; then
  [[ "$CLOUDFORGE_ACTION_REF" =~ ^[a-f0-9]{40}$ ]] || {
    echo 'Pin the released CloudForge Action to its full 40-character commit SHA.' >&2; exit 2;
  }
  [[ "$CLOUDFORGE_RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || exit 2
  candidate_path="$(mktemp -d "$RUNNER_TEMP/cloudforge-download.XXXXXX")"
  trap 'rm -rf "$candidate_path"' EXIT
  archive="cloudforge_${CLOUDFORGE_RELEASE_VERSION}_linux_amd64.tar.gz"
  for file in release.json checksums.txt "$archive"; do
    curl --fail --silent --show-error --location --connect-timeout 15 --max-time 300 \
      --output "$candidate_path/$file" "https://github.com/noor15102002/cloud-forge/releases/download/$CLOUDFORGE_RELEASE_VERSION/$file"
  done
  candidate_date="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["date"])' "$candidate_path/release.json")"
  candidate_sha="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["archive_sha256"])' "$candidate_path/release.json")"
  python3 "$action_root/scripts/install-candidate.py" "$candidate_path" "$install_dir" \
    --version "$CLOUDFORGE_RELEASE_VERSION" --commit "$CLOUDFORGE_ACTION_REF" \
    --date "$candidate_date" --sha256 "$candidate_sha" --require-enriched-manifest
else
  # Local uses: ./ remains available for development PR checks. It is not a release qualification.
  cd "$action_root"
  test "$(git rev-parse --show-toplevel)" = "$PWD"
  go build -trimpath -o "$install_dir/cloudforge" ./cmd/cloudforge
fi
echo "binary=$install_dir/cloudforge" >> "$GITHUB_OUTPUT"
