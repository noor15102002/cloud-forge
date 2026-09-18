#!/usr/bin/env bash
set -euo pipefail
containers="$(docker ps -aq --filter name=cloudforge-)"
images="$(docker image ls --format '{{.Repository}}:{{.Tag}}' | awk '/^cloudforge\//')"
networks="$(docker network ls --format '{{.Name}}' | awk '/^k3d-cloudforge-/')"
if [ -n "$containers$images$networks" ]; then
  echo "CloudForge-owned containers, images or networks remain after the trial." >&2
  exit 1
fi
