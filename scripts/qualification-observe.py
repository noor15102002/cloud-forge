#!/usr/bin/env python3
"""Timestamp a native Action invocation without retaining argv or environment."""
import sys
from pathlib import Path
from qualification_record import observation_started, observation_finished, write
operation, report = sys.argv[1:3]
if operation == "start":
    observation_started(report)
elif operation == "finish":
    code = int(sys.argv[3])
    observation_finished(report, code)
    write(Path(report).with_suffix(".exit.json"), {"exit_code": code, "completion_observed": True})
else:
    raise ValueError("unknown observation operation")
