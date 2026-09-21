#!/usr/bin/env python3
"""Pure fake-command checks for public cleanup capture; no Docker or app runs."""
from contextlib import contextmanager
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("pilot_cancellation", Path(__file__).with_name("pilot-cancellation.py"))
pilot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pilot)
NAME = "cloudforge-0123abcd"


class CleanupCaptureTests(unittest.TestCase):
    def test_stage_failure_still_retains_cleanup_observation(self):
        class EndedProcess:
            returncode = 2

            def poll(self):
                return self.returncode

        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            args = type("Arguments", (), {"binary": "/fake-cloudforge", "output": output, "monorepo": False,
                                          "force_builder_cleanup_failure": False})()
            environment = {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux"}
            result = subprocess.CompletedProcess(["bash"], 1, b"owned-cleanup-output\n", b"owned-cleanup-error\n")
            with patch.dict(os.environ, environment), patch.object(pilot.shutil, "which", return_value="/fake-tool"), \
                    patch.object(pilot.subprocess, "Popen", return_value=EndedProcess()), patch.object(pilot.subprocess, "run", return_value=result) as check:
                with self.assertRaisesRegex(pilot.helpers.QualificationError, "target_cancellation_phase_not_reached"):
                    pilot.run_stage(args, "redis", "unrelated", b"", {})
            check.assert_called_once()
            self.assertEqual((output / "cancel-redis.cleanup.stdout.txt").read_bytes(), result.stdout)
            self.assertEqual((output / "cancel-redis.cleanup.stderr.txt").read_bytes(), result.stderr)
            self.assertEqual(json.loads((output / "cancel-redis.cleanup.exit.json").read_text()), {"exit_code": 1, "completion_observed": True})
            self.assertEqual(json.loads((output / "cancel-redis.exit.json").read_text()), {"exit_code": 2})

    def test_spawn_not_observed_is_not_claimed_unexecuted(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            with patch.object(pilot.helpers.subprocess, "Popen", side_effect=OSError("fake spawn error")):
                with self.assertRaises(OSError):
                    pilot.helpers.tee_command(["/fake-tool"], output, "redis", None, None)
            record = self.record(output)
            self.assertIsNone(record["actual_command_executed"])
            self.assertIsNone(record["exit_code"])
            self.assertFalse(record["completion_observed"])

    @contextmanager
    def setup_case(self, mode="exit", code=17, force=False):
        with tempfile.TemporaryDirectory(prefix="cloudforge-cancel-") as temporary:
            root = Path(temporary).resolve()
            fixture = root / "app"
            pilot.fixture_copy(fixture, "redis", False)
            fake = root / "fake-tool"
            fake.write_text("""#!/usr/bin/env python3
import os,signal,sys,time
from pathlib import Path
with Path(os.environ['FAKE_CALLS']).open('a') as out: out.write('called\\n')
mode=os.environ['FAKE_MODE']
size=200000 if mode=='large' else 0
sys.stdout.buffer.write(b'O'*size+b'\\x1b[31mcleanup-out\\x00\\n');sys.stdout.buffer.flush()
sys.stderr.buffer.write(b'E'*size+b'\\x1b[32mcleanup-err\\x00\\n');sys.stderr.buffer.flush()
if mode=='signal': os.kill(os.getpid(),signal.SIGTERM)
if mode=='wait':
 signal.signal(signal.SIGINT,signal.SIG_DFL)
 while True: time.sleep(0.1)
sys.exit(int(os.environ['FAKE_EXIT']))
""")
            fake.chmod(0o700)
            output = root / "output"
            owner = root / "owner.json"
            pilot.helpers.write_json(owner, {"run_name": NAME})
            env = dict(os.environ, GITHUB_ACTIONS="true", RUNNER_ENVIRONMENT="github-hosted", RUNNER_OS="Linux",
                       FAKE_MODE=mode, FAKE_EXIT=str(code), FAKE_CALLS=str(root / "calls"))
            env.update({pilot.PREFIX+"STAGE":"redis", pilot.PREFIX+"FIXTURE":str(fixture), pilot.PREFIX+"MONOREPO":"false",
                        pilot.PREFIX+"OWNER":str(owner), pilot.PREFIX+"CLEANUP_OUTPUT":str(output),
                        pilot.PREFIX+"FORCE_BUILDER_FAILURE":str(force).lower(), pilot.PREFIX+"INJECTION":str(root / "injected.json"),
                        pilot.PREFIX+"MARKER":str(root / "marker.json"), "CLOUDFORGE_REAL_DOCKER":str(fake), "CLOUDFORGE_REAL_K3D":str(fake)})
            command = [sys.executable, str(Path(pilot.__file__)), "__tool", "docker", "buildx", "rm", "--force", NAME]
            yield root, output, env, command

    def record(self, output):
        records = list(output.glob("*.json"))
        self.assertEqual(len(records), 1)
        return json.loads(records[0].read_text())

    def test_exact_original_bytes_exit_and_native_deadline(self):
        for tool, args, timeout in (("docker", ["buildx", "rm", "--force", NAME], 60), ("k3d", ["cluster", "delete", NAME], 120)):
            for code in (0, 17):
                with self.subTest(tool=tool, code=code), self.setup_case(code=code) as (_, output, env, command):
                    result = subprocess.run(command[:3]+[tool]+args, env=env, capture_output=True, timeout=5)
                    self.assertEqual(result.returncode, code)
                    self.assertEqual(result.stdout,b"\x1b[31mcleanup-out\x00\n")
                    self.assertEqual(result.stderr,b"\x1b[32mcleanup-err\x00\n")
                    record=self.record(output)
                    self.assertEqual(record["arguments"],args)
                    self.assertEqual(record["native_timeout_seconds"],timeout)
                    self.assertEqual(record["exit_code"],code)
                    self.assertTrue(record["actual_command_executed"] and record["completion_observed"])
                    self.assertFalse(record["retry_performed"])
                    for name, raw in (("stdout",result.stdout),("stderr",result.stderr)):
                        self.assertEqual((output/record["streams"][name]["file"]).read_bytes(),raw)
                        self.assertFalse(record["streams"][name]["truncated"])

    def test_bounded_tail_preserves_all_forwarded_bytes(self):
        with self.setup_case(mode="large") as (_,output,env,command):
            result=subprocess.run(command,env=env,capture_output=True,timeout=5)
            record=self.record(output)
            for name,raw in (("stdout",result.stdout),("stderr",result.stderr)):
                self.assertGreater(len(raw),200000)
                stream=record["streams"][name]
                self.assertTrue(stream["truncated"])
                self.assertEqual(stream["observed_bytes"],len(raw))
                self.assertEqual((output/stream["file"]).read_bytes(),raw[-65536:])

    def test_child_signal_and_wrapper_signal_are_preserved(self):
        with self.setup_case(mode="signal") as (_,output,env,command):
            result=subprocess.run(command,env=env,capture_output=True,timeout=5)
            self.assertEqual(result.returncode,-signal.SIGTERM)
            self.assertEqual(self.record(output)["exit_code"],-signal.SIGTERM)
        for received in (signal.SIGINT,signal.SIGTERM,signal.SIGKILL):
            with self.subTest(signal=received),self.setup_case(mode="wait") as (_,output,env,command):
                process=subprocess.Popen(command,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
                try:
                    deadline=time.monotonic()+5
                    while time.monotonic()<deadline:
                        files=list(output.glob("*.json"))
                        if files and json.loads(files[0].read_text())["streams"].get("stderr",{}).get("observed_bytes",0):break
                        time.sleep(.01)
                    else:self.fail("bounded fake command not observed")
                    if received==signal.SIGKILL:os.killpg(process.pid,received)
                    else:process.send_signal(received)
                    stdout,stderr=process.communicate(timeout=5)
                    self.assertEqual(process.returncode,-received)
                    self.assertEqual(stdout,b"\x1b[31mcleanup-out\x00\n")
                    self.assertEqual(stderr,b"\x1b[32mcleanup-err\x00\n")
                    record=self.record(output)
                    self.assertEqual(record["completion_observed"],received!=signal.SIGKILL)
                    self.assertEqual(record["exit_code"],None if received==signal.SIGKILL else -received)
                finally:
                    if process.poll() is None:os.killpg(process.pid,signal.SIGKILL)
                    process.communicate(timeout=5)

    def test_unrelated_and_sensitive_commands_are_not_captured(self):
        for tool,args in (("k3d",["kubeconfig","get",NAME]),("docker",["inspect",NAME]),("docker",["buildx","rm","--force","unrelated"]),
                          ("docker",["buildx","rm","--force",NAME,"--unrecognized"]),("k3d",["cluster","delete",NAME,"other"])):
            with self.subTest(args=args),self.setup_case() as (_,output,env,command):
                result=subprocess.run(command[:3]+[tool]+args,env=env,capture_output=True,timeout=5)
                self.assertEqual(result.returncode,17)
                self.assertFalse(output.exists())

    def test_fixture_and_runner_guards_precede_command_or_artifacts(self):
        for fault in ("runner","source","symlink","fifo","parent","wrong-force-stage"):
            with self.subTest(fault=fault),self.setup_case() as (root,output,env,command):
                if fault=="runner":env["RUNNER_ENVIRONMENT"]="self-hosted"
                elif fault=="source":(root/"app/package.json").write_text("PRIVATE-CANARY")
                elif fault=="symlink":(root/"app/untrusted").symlink_to(root/"app/package.json")
                elif fault=="fifo":os.mkfifo(root/"app/fifo")
                elif fault=="parent":env[pilot.PREFIX+"FIXTURE"]=str(root/"other")
                else:
                    env[pilot.PREFIX+"FORCE_BUILDER_FAILURE"]="true"
                    env[pilot.PREFIX+"STAGE"]="build"
                result=subprocess.run(command,env=env,capture_output=True,timeout=5)
                self.assertEqual(result.returncode,2)
                self.assertNotIn(b"PRIVATE-CANARY",result.stderr)
                self.assertFalse((root/"calls").exists())
                self.assertFalse(output.exists())

    def test_artifact_failure_does_not_change_actual_cleanup(self):
        with self.setup_case() as (_,output,env,command):
            output.write_text("not directory")
            result=subprocess.run(command,env=env,capture_output=True,timeout=5)
            self.assertEqual(result.returncode,17)
            self.assertEqual(result.stderr,b"\x1b[32mcleanup-err\x00\n")

    def test_separate_explicit_fault_withholds_exactly_first_owned_removal(self):
        with self.setup_case(force=True) as (root,output,env,command):
            result=subprocess.run(command,env=env,capture_output=True,timeout=5)
            self.assertEqual(result.returncode,70)
            self.assertEqual(result.stdout,b"")
            self.assertEqual(result.stderr,pilot.INJECTED_ERROR)
            self.assertFalse((root/"calls").exists())
            record=self.record(output)
            self.assertTrue(record["injected"])
            self.assertFalse(record["actual_command_executed"])
            self.assertEqual(record["arguments"],command[4:])
            again=subprocess.run(command,env=env,capture_output=True,timeout=5)
            self.assertEqual(again.returncode,17)
            self.assertEqual((root/"calls").read_text(),"called\n")
            self.assertEqual(len(list(output.glob("*.json"))),2)

    def test_registration_and_arg_allowlist(self):
        base="memory=2g,cpu-period=100000,cpu-quota=200000"
        for options in (base,base+",env.CLOUDFORGE_RUN_ID="+NAME):
            self.assertEqual(pilot.created_run("docker",["buildx","create","--name",NAME,"--driver","docker-container","--driver-opt",options]),NAME)
        self.assertIsNone(pilot.created_run("docker",["buildx","create","--name",NAME,"--driver","docker-container","--driver-opt",base+",env.SECRET=private"]))
        self.assertIsNone(pilot.cleanup_operation("docker",["buildx","rm","--force",NAME],"other"))
        self.assertIsNone(pilot.cleanup_operation("docker",["container","rm","--force",NAME],NAME))
        self.assertEqual(pilot.CLEANUP_SECONDS,720)

    def test_main_guard_precedes_files_and_docker(self):
        with tempfile.TemporaryDirectory() as temporary:
            output=Path(temporary)/"absent"
            environment=dict(os.environ,GITHUB_ACTIONS="false")
            result=subprocess.run([sys.executable,pilot.__file__,"/missing",str(output),"--stages","redis"],env=environment,capture_output=True,timeout=5)
            self.assertEqual(result.returncode,2)
            self.assertFalse(output.exists())
            for arguments in (["--stages","build","--force-builder-cleanup-failure"],["--stages","redis","build","--force-builder-cleanup-failure"]):
                result=subprocess.run([sys.executable,pilot.__file__,"/missing",str(output),*arguments],env=environment,capture_output=True,timeout=5)
                self.assertEqual(result.returncode,2)
                self.assertFalse(output.exists())


if __name__=="__main__":unittest.main()
