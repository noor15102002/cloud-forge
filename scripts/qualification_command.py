"""Retain original public-fixture command streams before any assertion."""
import json
from pathlib import Path
import signal
import subprocess


def run_observed(command, report_path, *, timeout, env=None):
    report_path = Path(report_path)
    stderr_path = report_path.with_suffix(".stderr.txt")
    exit_path = report_path.with_suffix(".exit.json")
    if any(path.exists() for path in (report_path, stderr_path, exit_path)):
        raise ValueError("qualification output already exists; preserve the earlier attempt and choose a new directory")
    process, timed_out = None, False
    try:
        with report_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=env)
            try:
                process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                timed_out = True
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=720)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=15)
    finally:
        exit_path.write_text(json.dumps({"exit_code": process.returncode if process else None,
                                        "completion_observed": process is not None and process.returncode is not None,
                                        "outer_timeout": timed_out}, indent=2) + "\n")
    result = subprocess.CompletedProcess(command, process.returncode, report_path.read_text(), stderr_path.read_text())
    if timed_out:
        raise subprocess.TimeoutExpired(command, timeout, output=result.stdout, stderr=result.stderr)
    return result
