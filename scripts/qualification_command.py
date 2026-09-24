"""Retain original public-fixture command streams before any assertion."""
import json
from pathlib import Path
import signal
import subprocess
from qualification_record import observation_started, observation_finished, active_session, settle_session, signal_session, force_session_closed


def run_observed(command, report_path, *, timeout, env=None, own_session=False):
    report_path = Path(report_path)
    stderr_path = report_path.with_suffix(".stderr.txt")
    exit_path = report_path.with_suffix(".exit.json")
    if any(path.exists() for path in (report_path, stderr_path, exit_path)):
        raise ValueError("qualification output already exists; preserve the earlier attempt and choose a new directory")
    process, timed_out, settled = None, False, False
    observation_started(report_path)
    try:
        with report_path.open("xb") as stdout, stderr_path.open("xb") as stderr:
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=env, start_new_session=own_session)
            try:
                process.wait(timeout=timeout)
            except (subprocess.TimeoutExpired, KeyboardInterrupt) as interruption:
                timed_out = True
                if own_session:
                    signal_session(process, signal.SIGINT)
                    settled = settle_session(process, 720)
                else:
                    if isinstance(interruption, subprocess.TimeoutExpired):
                        process.send_signal(signal.SIGINT)
                    try:
                        process.wait(timeout=720)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=15)
    finally:
        if own_session and process is not None and not settled:
            try:
                if active_session(process.pid):
                    timed_out = True
                    signal_session(process, signal.SIGINT)
                    settled = settle_session(process, 0)
                else:
                    settled = True
            except (OSError, ValueError, subprocess.SubprocessError):
                timed_out = True
                settled = force_session_closed(process)
        observation_finished(report_path, process.returncode if process else None)
        exit_path.write_text(json.dumps({"exit_code": process.returncode if process else None,
                                        "completion_observed": process is not None and process.returncode is not None,
                                        "outer_timeout": timed_out, **({"process_session_settled": settled} if own_session else {})}, indent=2) + "\n")
    result = subprocess.CompletedProcess(command, process.returncode, report_path.read_text(), stderr_path.read_text())
    if timed_out:
        raise subprocess.TimeoutExpired(command, timeout, output=result.stdout, stderr=result.stderr)
    return result
