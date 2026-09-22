#!/usr/bin/env python3
"""Write the small result before upload, then retain the actual upload outcome."""
import argparse
import os
import shutil
from pathlib import Path
import sys
from qualification_record import collect, summary, write

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--records", type=Path, required=True)
parser.add_argument("--cleanup", type=Path, required=True)
parser.add_argument("--output", type=Path, required=True)
parser.add_argument("--bulk-outcome", default="pending")
parser.add_argument("--small-outcome", default="pending")
parser.add_argument("--gate", action="store_true")
parser.add_argument("--stage-evidence", type=Path)
args = parser.parse_args()
states = {"success": "COMPLETE", "failure": "FAILED", "cancelled": "UNKNOWN", "skipped": "UNKNOWN", "pending": "UNKNOWN"}
value = collect(args.records, args.cleanup, states.get(args.bulk_outcome, "UNKNOWN"), states.get(args.small_outcome, "UNKNOWN"))
write(args.output / "qualification.json", value)
text = summary(value)
(args.output / "summary.md").write_text(text)
if os.environ.get("GITHUB_STEP_SUMMARY"):
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as output:
        output.write(text)
print(text)
if args.stage_evidence:
    args.stage_evidence.mkdir(parents=True, exist_ok=True)
    destination = args.stage_evidence / "qualification-records"
    if destination.resolve() != args.records.resolve():
        destination.mkdir(exist_ok=True)
        for source in args.records.iterdir():
            if source.is_file() and source.suffix in (".json", ".txt"):
                shutil.copyfile(source, destination / source.name)
    if args.cleanup.is_file():
        shutil.copyfile(args.cleanup, args.stage_evidence / "independent-cleanup.json")
if args.gate and value["qualification_status"] != "PASS":
    print("::error::Qualification did not pass; inspect the independent native, assertions, cleanup and artifact states.")
    sys.exit(1)
