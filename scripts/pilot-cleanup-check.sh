#!/usr/bin/env bash
set -euo pipefail
containers="$(docker ps -aq --filter name=cloudforge-)"
images="$(docker image ls --format '{{.Repository}}:{{.Tag}}' | awk '/^cloudforge\//')"
networks="$(docker network ls --format '{{.Name}}' | awk '/^k3d-cloudforge-/')"
volumes="$(docker volume ls --format '{{.Name}}' | awk '/^(k3d-cloudforge-|buildx_buildkit_cloudforge-)/')"
if [ -n "$containers$images$networks$volumes" ]; then
  echo "CloudForge-owned resources remain after the trial." >&2
  docker ps -a --filter name=cloudforge- --format '{{.ID}} {{.Names}}' >&2
  docker network ls --filter name=cloudforge- >&2
  docker volume ls --filter name=cloudforge- >&2
  exit 1
fi
