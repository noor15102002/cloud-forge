#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cloudforge="${1:-$repository_root/bin/cloudforge}"
if [ ! -x "$cloudforge" ]; then
  echo "Build CloudForge first or pass the path to an executable binary." >&2
  exit 2
fi
for required in git jq; do
  command -v "$required" >/dev/null || {
    echo "$required is required for external validation." >&2
    exit 2
  }
done

workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT

validate() {
  local name="$1" repository="$2" commit="$3" application_path="$4" language="$5" framework="$6" dependency="${7:-}"
  local checkout="$workspace/$name"
  local report="$workspace/$name.json"

  git init --quiet "$checkout"
  git -C "$checkout" remote add origin "$repository"
  git -C "$checkout" sparse-checkout init --cone
  git -C "$checkout" sparse-checkout set "$application_path"
  git -C "$checkout" fetch --quiet --depth 1 --filter=blob:none origin "$commit"
  GIT_LFS_SKIP_SMUDGE=1 git -C "$checkout" -c advice.detachedHead=false -c core.hooksPath=/dev/null checkout --quiet --detach FETCH_HEAD

  "$cloudforge" analyze "$checkout/$application_path" --format json > "$report"
  jq --exit-status \
    --arg language "$language" \
    --arg framework "$framework" \
    '(.supported == true) and any(.application.runtimes[]; .language == $language and .framework == $framework)' \
    "$report" >/dev/null
  if [ -n "$dependency" ]; then
    jq --exit-status --arg dependency "$dependency" \
      'any(.application.dependencies[]; .name == $dependency)' "$report" >/dev/null
  fi
  printf '%s\t%s\t%s\t%s\n' "$name" "$commit" "$language" "$framework"
}

printf 'repository\tcommit\tlanguage\tframework\n'
validate \
  node-bulletin-board \
  https://github.com/dockersamples/node-bulletin-board.git \
  4aaaa14f11cb9b45fe1a1b9db3edfdb5d9a4a13e \
  bulletin-board-app \
  nodejs \
  express
validate \
  nextjs-with-docker \
  https://github.com/kconner/next-js-in-docker-example.git \
  a2daae78c5612ee63de97a03491f1291edf3f588 \
  . \
  typescript \
  next.js
validate \
  google-cloud-run-python \
  https://github.com/GoogleCloudPlatform/python-docs-samples.git \
  5e143effc56aeb9991f86c1d5dc2ba16c2aee4c3 \
  run/helloworld \
  python \
  flask
validate \
  full-stack-fastapi-backend \
  https://github.com/fastapi/full-stack-fastapi-template.git \
  cb740b656d7a0a6c5e12c7bf8e50343ec94ee9c7 \
  backend \
  python \
  fastapi \
  postgresql
