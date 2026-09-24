#!/usr/bin/env python3
"""Run an existing harness with an explicit required-report plan and bounded time."""
import argparse
from pathlib import Path
import sys
from qualification_record import run_case

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--case", required=True)
parser.add_argument("--evidence", type=Path, required=True)
parser.add_argument("--records", type=Path, required=True)
parser.add_argument("--binary", type=Path, required=True)
parser.add_argument("--timeout", type=int, default=6600)
parser.add_argument("command", nargs=argparse.REMAINDER)
args = parser.parse_args()
command = args.command[1:] if args.command[:1] == ["--"] else args.command
if not command:
    parser.error("a harness command is required")
sys.exit(run_case(command, args.case, args.evidence, args.records, args.binary, args.timeout))
