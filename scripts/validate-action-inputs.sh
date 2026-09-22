#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "$1" >&2
  exit 2
}

validate_artifact_name() {
  local value="$1" label="$2"
  if [ -z "$value" ] || [[ "$value" == *'"'* || "$value" == *':'* || "$value" == *'<'* || "$value" == *'>'* || "$value" == *'|'* || "$value" == *'*'* || "$value" == *'?'* || "$value" == *'/'* || "$value" == *'\\'* || "$value" == *$'\r'* || "$value" == *$'\n'* ]]; then
    fail "$label contains a character that GitHub artifact names do not support."
  fi
}

if [ "${RUNNER_OS:-}" != "Linux" ] || [ "${RUNNER_ARCH:-}" != "X64" ]; then
  fail "CloudForge verification currently supports GitHub-hosted Ubuntu x64 runners."
fi
if [ -n "$CLOUDFORGE_BASELINE_PATH" ] && [ -n "$CLOUDFORGE_BASELINE_RUN_ID" ]; then
  fail "Set either baseline-path or baseline-run-id, not both."
fi
if [ -z "$CLOUDFORGE_APPLICATION_PATH" ] || [[ "$CLOUDFORGE_APPLICATION_PATH" == *$'\n'* || "$CLOUDFORGE_APPLICATION_PATH" == *$'\r'* ]]; then
  fail "path must be a nonempty single-line value."
fi
if [[ "$CLOUDFORGE_BASELINE_PATH" == *$'\n'* || "$CLOUDFORGE_BASELINE_PATH" == *$'\r'* ]]; then
  fail "baseline-path must not contain newline characters."
fi
if [ -n "$CLOUDFORGE_BASELINE_RUN_ID" ]; then
  if [[ ! "$CLOUDFORGE_BASELINE_RUN_ID" =~ ^[1-9][0-9]*$ ]] || [ "${#CLOUDFORGE_BASELINE_RUN_ID}" -gt 16 ] || { [ "${#CLOUDFORGE_BASELINE_RUN_ID}" -eq 16 ] && [[ "$CLOUDFORGE_BASELINE_RUN_ID" > "9007199254740991" ]]; }; then
    fail "baseline-run-id must be a positive JavaScript-safe integer."
  fi
  if [ "$CLOUDFORGE_HAS_GITHUB_TOKEN" != "true" ]; then
    fail "github-token is required when baseline-run-id is set."
  fi
  if [ -z "$CLOUDFORGE_BASELINE_ARTIFACT_NAME" ]; then
    fail "baseline-artifact-name must not be empty when baseline-run-id is set."
  fi
  validate_artifact_name "$CLOUDFORGE_BASELINE_ARTIFACT_NAME" "baseline-artifact-name"
fi
if [[ "${CLOUDFORGE_UPLOAD_ARTIFACT:-}" != true && "${CLOUDFORGE_UPLOAD_ARTIFACT:-}" != false ]]; then
  fail "upload-artifact must be exactly true or false."
fi
validate_artifact_name "$CLOUDFORGE_ARTIFACT_NAME" "artifact-name"
if [[ ! "$CLOUDFORGE_RETENTION_DAYS" =~ ^([1-9]|[1-8][0-9]|90)$ ]]; then
  fail "retention-days must be an integer from 1 through 90."
fi
if [[ "$CLOUDFORGE_APPLICATION_PATH" = /* ]]; then
  fail "path must be relative to the GitHub workspace."
fi
workspace="$(realpath -e -- "$GITHUB_WORKSPACE")"
if ! application="$(realpath -e -- "$workspace/$CLOUDFORGE_APPLICATION_PATH")" || [ ! -d "$application" ]; then
  fail "path must identify an existing directory in the GitHub workspace."
fi
case "$application/" in
  "$workspace/"*) ;;
  *) fail "path must not escape the GitHub workspace." ;;
esac
printf 'application-path=%s\n' "$application" >> "$GITHUB_OUTPUT"

config="${CLOUDFORGE_CONFIG_PATH:-}"
if [ -n "$config" ]; then
  if [[ "$config" = /* || "$config" == *$'\n'* || "$config" == *$'\r'* ]]; then
    fail "config-path must be a relative single-line workspace path."
  fi
  if ! config="$(realpath -e -- "$workspace/$config")" || [ ! -f "$config" ]; then
    fail "config-path must identify a regular file in the GitHub workspace."
  fi
  case "$config" in
    "$workspace/"*) ;;
    *) fail "config-path must not escape the GitHub workspace." ;;
  esac
fi
printf 'config-path=%s\n' "$config" >> "$GITHUB_OUTPUT"
