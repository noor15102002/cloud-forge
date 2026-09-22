#!/usr/bin/env python3
"""Declare and finish the packaged Action's own native qualification case."""
import argparse
import hashlib
from pathlib import Path
from qualification_record import initialize, now, read, write
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("operation", choices=("start", "finish"))
parser.add_argument("--binary", type=Path, required=True)
parser.add_argument("--evidence", type=Path, required=True)
parser.add_argument("--records", type=Path, required=True)
parser.add_argument("--outcome", choices=("success", "failure"), default="failure")
args = parser.parse_args()
if args.operation == "start":
    initialize("action-native", args.evidence, args.records, args.binary)
    write(args.records / "assertions.json", {"started_at": now(), "status": "UNKNOWN", "completion_observed": False})
else:
    plan, result = read(args.records / "plan.json"), read(args.records / "assertions.json")
    result.update(process_session_settled=read(args.evidence / "verification.exit.json").get("process_session_settled") is True, finished_at=now(), status="PASS" if args.outcome == "success" else "FAIL", completion_observed=True,
                  binary_unchanged=hashlib.sha256(args.binary.read_bytes()).hexdigest() == plan.get("binary_sha256"))
    write(args.records / "assertions.json", result)
