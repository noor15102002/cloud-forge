#!/usr/bin/env python3
"""Catch composite schema gaps and preserve runtime cleanup-before-upload order."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent.parent
# GitHub runner action_yaml.json defines these composite run-step/uses-step fields.
# Pinned source: actions/runner@50bd7667ef037c0bef8fdc38337a7a797f2279c7.
# Full schema and content response are preserved in the release evidence.
COMMON = {"name", "id", "if", "env", "continue-on-error"}
RUN_FIELDS = COMMON | {"run", "working-directory", "shell"}
USES_FIELDS = COMMON | {"uses", "with"}


def composite_fields(text):
    blocks = re.split(r"(?m)^    - ", text)[1:]
    for block in blocks:
        keys = set(re.findall(r"(?m)^(?:      )?([a-z][a-z-]*):", block))
        allowed = RUN_FIELDS if "run" in keys else USES_FIELDS
        unknown = keys - allowed
        if unknown:
            raise ValueError("Unsupported composite step fields: " + ", ".join(sorted(unknown)))


class WorkflowContracts(unittest.TestCase):
    def test_all_composites_use_only_supported_step_fields(self):
        for path in [ROOT / "action.yml", *(ROOT / ".github/actions").glob("*/action.yml")]:
            with self.subTest(path=str(path.relative_to(ROOT))):
                composite_fields(path.read_text())

    def test_composite_timeout_is_rejected_despite_actionlint_acceptance(self):
        with self.assertRaisesRegex(ValueError, "timeout-minutes"):
            composite_fields("runs:\n  using: composite\n  steps:\n    - uses: actions/upload-artifact@pinned\n      timeout-minutes: 2\n")
        composite_fields("runs:\n  steps:\n    - run: true\n      shell: bash\n      continue-on-error: true\n")

    def test_runtime_workflows_cleanup_then_small_then_bounded_bulk_upload(self):
        total = 0
        for filename in ("pilot.yml", "release-candidate.yml", "integration.yml", "monorepo.yml", "backend.yml", "worker.yml", "dependencies.yml", "reliability.yml", "operational.yml"):
            text = (ROOT / ".github/workflows" / filename).read_text()
            for job in re.split(r"(?m)^  [a-z][a-z0-9-]*:\n", text)[1:]:
                if "--capture-baseline" not in job:
                    continue
                total += 1
                with self.subTest(workflow=filename, job=job[:80]):
                    self.assertLess(job.index("--capture-baseline"), job.index("--baseline"))
                    self.assertLess(job.index("--baseline"), job.index("uses: actions/upload-artifact@"))
                    self.assertLess(job.index("--output \"$RECORDS/initial\""), job.index("uses: actions/upload-artifact@"))
                    uploads = [step for step in re.split(r"(?m)^      - ", job) if "uses: actions/upload-artifact@" in step]
                    self.assertGreaterEqual(len(uploads), 3)
                    for upload in uploads:
                        self.assertRegex(upload, r"timeout-minutes: [125]\n")
                        self.assertIn("continue-on-error: true", upload)
                        self.assertIn("if-no-files-found: error", upload)
                    self.assertIn('--bulk-outcome "$BULK" --small-outcome "$SMALL" --gate', job)
                    self.assertIn('if [ "$FINAL" != success ]', job)
        self.assertGreaterEqual(total, 16)

    def test_candidate_first_archive_only_and_separate_clean_compiler_caches(self):
        text = (ROOT / ".github/workflows/release-candidate.yml").read_text()
        self.assertIn('GOCACHE="$RUNNER_TEMP/canonical-go-cache" bash scripts/build-release.sh', text)
        self.assertIn('GOCACHE="$RUNNER_TEMP/reproduced-go-cache" bash scripts/build-release.sh', text)
        self.assertIn('scripts/compare-releases.py', text)
        self.assertIn('for key in ("version", "commit", "date", "archive_sha256")', text)
        self.assertNotIn('diff --recursive', text)
        self.assertIn('report-path: ${{ runner.temp }}/native-evidence/native/verification.json', text)

    def test_worker_cleanup_baseline_survives_observer_or_fixture_self_test_failure(self):
        text = (ROOT / ".github/workflows/worker.yml").read_text()
        steps = re.split(r"(?m)^      - ", text)[1:]
        baseline = next(index for index, step in enumerate(steps) if "--capture-baseline" in step)
        for check in ("scripts/pilot-worker.py --self-test", "node --check testdata/healthy-worker/worker.js"):
            with self.subTest(check=check):
                validation = next(index for index, step in enumerate(steps) if check in step)
                self.assertLess(baseline, validation)
        cleanup = next(step for step in steps if '--baseline "$RUNNER_TEMP/cleanup-baseline.json"' in step)
        self.assertIn("if: always()", cleanup)


if __name__ == "__main__":
    unittest.main()
