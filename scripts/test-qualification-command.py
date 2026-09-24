#!/usr/bin/env python3
"""Command preservation must survive failures, timeouts and attempted overwrites."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from qualification_command import run_observed


class NativeEvidenceTests(unittest.TestCase):
    def test_failure_preserves_original_streams_exit_and_refuses_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "case.json"
            command = [sys.executable, "-c", "import sys; print('original stdout'); print('original stderr', file=sys.stderr); sys.exit(7)"]
            result = run_observed(command, report, timeout=5)
            self.assertEqual(result.returncode, 7)
            self.assertEqual(report.read_text(), "original stdout\n")
            self.assertEqual(report.with_suffix(".stderr.txt").read_text(), "original stderr\n")
            self.assertEqual(json.loads(report.with_suffix(".exit.json").read_text()), {"exit_code": 7, "completion_observed": True, "outer_timeout": False})
            with self.assertRaisesRegex(ValueError, "already exists"):
                run_observed(command, report, timeout=5)
            self.assertEqual(report.read_text(), "original stdout\n")

    def test_timeout_retains_partial_output_and_marks_timeout(self):
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "case.json"
            command = [sys.executable, "-c", "import signal,time; signal.signal(signal.SIGINT, lambda *_: exit(2)); print('partial', flush=True); time.sleep(10)"]
            with self.assertRaises(subprocess.TimeoutExpired):
                run_observed(command, report, timeout=0.5)
            self.assertEqual(report.read_text(), "partial\n")
            record = json.loads(report.with_suffix(".exit.json").read_text())
            self.assertEqual(record, {"exit_code": 2, "completion_observed": True, "outer_timeout": True})


if __name__ == "__main__":
    unittest.main()
