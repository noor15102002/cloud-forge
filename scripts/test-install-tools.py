#!/usr/bin/env python3
"""Exercise both installer modes with local upstream-shaped download fixtures."""

import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().with_name("install-tools.sh")


class InstallToolsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.upstream = self.root / "upstream"
        self.upstream.mkdir()
        self.bin = self.root / "fake-bin"
        self.bin.mkdir()
        self.destination = self.root / "tools with spaces"
        self.github_path = self.root / "github-path"
        self.marker = self.root / "downloads"
        self.env = dict(os.environ)
        self.env.pop("GITHUB_PATH", None)
        self.env.update(PATH=f"{self.bin}:{os.environ['PATH']}",
                        RUNNER_TEMP=str(self.root), RUNNER_OS="Linux", RUNNER_ARCH="X64")
        self.write_executable("uname", '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
        self.write_executable("curl", """#!/usr/bin/env python3
import os, pathlib, shutil, sys
args = sys.argv[1:]
pathlib.Path(os.environ["TEST_DOWNLOAD_MARKER"]).touch()
name = args[-1].rsplit("/", 1)[-1]
if "k3d-io" in args[-1] and name == "checksums.txt":
    name = "k3d-checksums.txt"
shutil.copyfile(pathlib.Path(os.environ["TEST_UPSTREAM"]) / name, args[args.index("--output") + 1])
""")
        self.env.update(TEST_DOWNLOAD_MARKER=str(self.marker), TEST_UPSTREAM=str(self.upstream))
        self.fixtures()

    def write_executable(self, name, source):
        path = self.bin / name
        path.write_text(source)
        path.chmod(0o755)

    def fixtures(self):
        payload = b"#!/bin/sh\nexit 0\n"
        digest = hashlib.sha256(payload).hexdigest()
        (self.upstream / "k3d-linux-amd64").write_bytes(payload)
        (self.upstream / "k3d-checksums.txt").write_text(f"{digest}  _dist/k3d-linux-amd64\n")
        (self.upstream / "kubectl").write_bytes(payload)
        (self.upstream / "kubectl.sha256").write_text(digest)
        for archive, member, checksums in (
            ("trivy_0.74.0_Linux-64bit.tar.gz", "trivy", "trivy_0.74.0_checksums.txt"),
            ("k6-v2.2.0-linux-amd64.tar.gz", "k6-v2.2.0-linux-amd64/k6", "k6-v2.2.0-checksums.txt"),
        ):
            with tarfile.open(self.upstream / archive, "w:gz") as output:
                info = tarfile.TarInfo(member)
                info.size = len(payload)
                info.mode = 0o755
                output.addfile(info, io.BytesIO(payload))
            checksum = hashlib.sha256((self.upstream / archive).read_bytes()).hexdigest()
            (self.upstream / checksums).write_text(f"{checksum}  {archive}\n")

    def run_installer(self, *arguments):
        return subprocess.run(["bash", str(SCRIPT), *map(str, arguments)], env=self.env,
                              cwd=self.root, capture_output=True, text=True, check=False)

    def assert_no_install(self, result):
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertFalse(self.marker.exists(), "invalid invocation reached downloads")
        self.assertFalse(self.destination.exists(), "invalid invocation created installation directory")

    def test_invalid_arguments_stop_before_download(self):
        for arguments in ((), ("--local",), ("",), ("--local", ""),
                          ("--unknown",), ("--local", "--unknown"),
                          (self.destination, "extra"), ("--local", self.destination, "extra")):
            with self.subTest(arguments=arguments):
                self.assert_no_install(self.run_installer(*arguments))

    def test_ci_requires_output_path_before_installation(self):
        self.assert_no_install(self.run_installer(self.destination))

    def test_ci_rejects_other_platform_before_installation(self):
        self.env.update(GITHUB_PATH=str(self.github_path), RUNNER_ARCH="ARM64")
        self.assert_no_install(self.run_installer(self.destination))

    def test_local_checks_actual_platform_before_installation(self):
        self.write_executable("uname", '#!/bin/sh\necho Darwin\n')
        self.assert_no_install(self.run_installer("--local", self.destination))

    def test_local_installs_checked_files_without_ci_environment(self):
        self.github_path.write_text("untouched\n")
        self.env["GITHUB_PATH"] = str(self.github_path)
        result = self.run_installer("--local", self.destination)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines()[-1], str(self.destination / "bin"))
        self.assertEqual(self.github_path.read_text(), "untouched\n")
        self.assertEqual(sorted(path.name for path in (self.destination / "bin").iterdir()),
                         ["k3d", "k6", "kubectl", "trivy"])
        self.assertFalse(list(self.root.glob("cloudforge-tools.*")), "download directory was retained")

    def test_default_ci_mode_appends_path(self):
        self.env["GITHUB_PATH"] = str(self.github_path)
        result = self.run_installer(self.destination)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.github_path.read_text(), f"{self.destination / 'bin'}\n")

    def test_relative_local_directory_is_resolved_before_download_directory(self):
        result = self.run_installer("--local", "local-tools")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.root / "local-tools/bin/trivy").is_file())
        self.assertEqual(result.stdout.splitlines()[-1], str(self.root / "local-tools/bin"))

    def test_bad_upstream_checksum_rejects_tool(self):
        (self.upstream / "k3d-linux-amd64").write_bytes(b"altered payload")
        result = self.run_installer("--local", self.destination)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.destination / "bin" / "k3d").exists())
        self.assertFalse(list(self.root.glob("cloudforge-tools.*")), "failed download directory was retained")


if __name__ == "__main__":
    unittest.main()
