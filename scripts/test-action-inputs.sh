#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
validator="$repository_root/scripts/validate-action-inputs.sh"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT
workspace="$temporary/workspace"
outside="$temporary/outside"
mkdir -p "$workspace/application" "$outside"
ln -s "$outside" "$workspace/escape"

defaults=(
  "RUNNER_OS=Linux"
  "RUNNER_ARCH=X64"
  "GITHUB_WORKSPACE=$workspace"
  "CLOUDFORGE_APPLICATION_PATH=application"
  "CLOUDFORGE_ARTIFACT_NAME=cloudforge-verification"
  "CLOUDFORGE_BASELINE_ARTIFACT_NAME=cloudforge-verification"
  "CLOUDFORGE_BASELINE_PATH="
  "CLOUDFORGE_BASELINE_RUN_ID="
  "CLOUDFORGE_HAS_GITHUB_TOKEN=false"
  "CLOUDFORGE_RETENTION_DAYS=14"
  "CLOUDFORGE_UPLOAD_ARTIFACT=true"
)

expect_failure() {
  local expected="$1"
  shift
  local output_file="$temporary/failure"
  local output_path="$temporary/output"
  set +e
  env "${defaults[@]}" "GITHUB_OUTPUT=$output_path" "$@" "$validator" >"$output_file" 2>&1
  local status="$?"
  set -e
  if [ "$status" -eq 0 ]; then
    echo "input validation unexpectedly succeeded: $expected" >&2
    exit 1
  fi
  if [ "$status" -ne 2 ] || ! grep -Fq "$expected" "$output_file"; then
    echo "input validation did not report the expected error: $expected" >&2
    cat "$output_file" >&2
    exit 1
  fi
}

valid_output="$temporary/valid-output"
env "${defaults[@]}" "GITHUB_OUTPUT=$valid_output" "$validator"
grep -Fxq "application-path=$workspace/application" "$valid_output"

baseline_output="$temporary/baseline-output"
env "${defaults[@]}" "GITHUB_OUTPUT=$baseline_output" \
  CLOUDFORGE_BASELINE_RUN_ID=9007199254740991 \
  CLOUDFORGE_HAS_GITHUB_TOKEN=true \
  "$validator"

expect_failure "relative to the GitHub workspace" CLOUDFORGE_APPLICATION_PATH=/tmp
expect_failure "nonempty single-line" CLOUDFORGE_APPLICATION_PATH=
expect_failure "nonempty single-line" $'CLOUDFORGE_APPLICATION_PATH=application\nforged-output=value'
expect_failure "must not escape" CLOUDFORGE_APPLICATION_PATH=escape
expect_failure "artifact-name contains" CLOUDFORGE_ARTIFACT_NAME=bad/name
expect_failure "retention-days" CLOUDFORGE_RETENTION_DAYS=91
expect_failure "JavaScript-safe integer" CLOUDFORGE_BASELINE_RUN_ID=9007199254740992 CLOUDFORGE_HAS_GITHUB_TOKEN=true
expect_failure "github-token is required" CLOUDFORGE_BASELINE_RUN_ID=42
expect_failure "baseline-artifact-name must not be empty" CLOUDFORGE_BASELINE_RUN_ID=42 CLOUDFORGE_HAS_GITHUB_TOKEN=true CLOUDFORGE_BASELINE_ARTIFACT_NAME=

printf 'schema_version: v1alpha3\n' > "$workspace/config.yaml"
printf 'schema_version: v1alpha3\n' > "$outside/config.yaml"
mkfifo "$workspace/fifo"
config_output="$temporary/config-output"
env "${defaults[@]}" "GITHUB_OUTPUT=$config_output" CLOUDFORGE_CONFIG_PATH=config.yaml "$validator"
grep -Fxq "config-path=$workspace/config.yaml" "$config_output"
expect_failure "relative single-line" CLOUDFORGE_CONFIG_PATH=/tmp/config.yaml
expect_failure "relative single-line" $'CLOUDFORGE_CONFIG_PATH=config.yaml\nforged-output=value'
expect_failure "must not escape" CLOUDFORGE_CONFIG_PATH=escape/config.yaml
expect_failure "regular file" CLOUDFORGE_CONFIG_PATH=fifo
expect_failure "regular file" CLOUDFORGE_CONFIG_PATH=application

# A typo must not silently disable both default upload and qualification records.
for value in '' TRUE False yes tru; do
  expect_failure "upload-artifact must be exactly" "CLOUDFORGE_UPLOAD_ARTIFACT=$value"
done
env "${defaults[@]}" "GITHUB_OUTPUT=$temporary/deferred-output" CLOUDFORGE_UPLOAD_ARTIFACT=false "$validator"
