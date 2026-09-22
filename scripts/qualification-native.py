#!/usr/bin/env python3
"""Bound an Action qualification invocation while retaining its actual child exit."""
import argparse
from pathlib import Path
import subprocess
import sys
from qualification_command import run_observed
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--report", type=Path, required=True)
parser.add_argument("--timeout", type=int, required=True)
parser.add_argument("command", nargs=argparse.REMAINDER)
args = parser.parse_args()
command = args.command[1:] if args.command[:1] == ["--"] else args.command
try:
    result = run_observed(command, args.report, timeout=args.timeout, own_session=True)
except subprocess.TimeoutExpired:
    # Native exit remains in the sidecar. This operational wrapper outcome is separate.
    sys.exit(2)
sys.exit(result.returncode if result.returncode >= 0 else 2)
