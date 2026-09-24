#!/usr/bin/env bash
# Read-only independent remnant observation; never deletes by name.
set -euo pipefail
python3 "$(dirname "$0")/pilot-cleanup-check.py" "$@"
