#!/usr/bin/env python3
"""Bounded loopback regression tests for the generic network qualification helper.

No Docker, Kubernetes or application repository code is executed. The real
helper binds only 127.0.0.1 on an OS-selected port; Kubernetes observations use a
fake adapter and retain no logs or arbitrary container messages.
"""
import importlib.util
import json
import os
from pathlib import Path
import select
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import unittest

REPOSITORY = Path(__file__).resolve().parent.parent
NODE = os.environ.get("NODE_BINARY") or shutil.which("node")
sys.dont_write_bytecode = True
specification = importlib.util.spec_from_file_location("backend_pilot", REPOSITORY / "scripts/pilot-backend.py")
pilot = importlib.util.module_from_spec(specification)
specification.loader.exec_module(pilot)


class NetworkCanaryTests(unittest.TestCase):
    def test_repeated_reset_clients_do_not_kill_live_positive_control(self):
        self.assertTrue(NODE, "Node is required to test the actual helper")
        command = [NODE, str(REPOSITORY / "testdata/backend-http/network-probe.js"), "listen", "127.0.0.1", "0"]
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertTrue(select.select([process.stdout], [], [], 5)[0], "listener did not announce its bound port")
            address = json.loads(process.stdout.readline())
            self.assertEqual(address["address"], "127.0.0.1")
            self.assertTrue(address["listening"])
            self.assertGreater(address["port"], 0)
            endpoint = ("127.0.0.1", address["port"])
            deadline = time.monotonic() + 10

            def live_control():
                self.assertLess(time.monotonic(), deadline, "reset regression exceeded its time bound")
                with socket.create_connection(endpoint, timeout=1) as client:
                    chunks = []
                    while True:
                        chunk = client.recv(128)
                        if not chunk:
                            break
                        chunks.append(chunk)
                    self.assertEqual(b"".join(chunks), b"canary\n")
                self.assertIsNone(process.poll(), "canary exited after a client reset")

            live_control()
            for _ in range(8):
                for _ in range(64):
                    self.assertLess(time.monotonic(), deadline)
                    with socket.create_connection(endpoint, timeout=1) as client:
                        # A zero linger close sends RST, matching connect-only
                        # probes that leave without consuming the canary bytes.
                        client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
                live_control()
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=2)
            _, errors = process.communicate(timeout=1)
            self.assertNotIn("ECONNRESET", errors)
            self.assertNotIn("Unhandled", errors)

    def test_lost_control_retains_safe_termination_state_and_cleans_probes(self):
        for cleanup_error in (False, True):
            with self.subTest(cleanup_error=cleanup_error), tempfile.TemporaryDirectory(prefix="cf-canary-state-test-") as directory:
                output = Path(directory)
                controls = 0
                deleted = []
                secret_message = "ARBITRARY-CONTAINER-MESSAGE-MUST-NOT-BE-RETAINED"

                def kube(*args, payload=None, timeout=15):
                    nonlocal controls
                    self.assertNotIn("logs", args)
                    if args[0] in ("apply", "wait"):
                        return ""
                    if args[:2] == ("get", "pod"):
                        return json.dumps({"status": {"podIP": "192.0.2.1", "phase": "Failed", "containerStatuses": [
                            {"name": "probe", "ready": False, "restartCount": 0,
                             "state": {"terminated": {"exitCode": 1, "signal": 0, "message": secret_message}},
                             "lastState": {}, "image": secret_message}]}})
                    if args[0] == "exec":
                        if args[1].endswith("-control"):
                            controls += 1
                            connected = controls == 1
                        else:
                            connected = str(args[-2]).endswith(".cloudforge.svc.cluster.local")
                        return json.dumps({"connected": connected})
                    if args[0] == "delete":
                        deleted.append(args[1])
                        if cleanup_error and args[1] == "pod":
                            raise RuntimeError(secret_message)
                        return ""
                    self.fail("Unexpected adapter operation")

                with self.assertRaisesRegex(RuntimeError, "lost their live positive control"):
                    pilot.qualify_network(kube, "local-test", output)
                raw = (output / "network-enforcement.json").read_text()
                result = json.loads(raw)
                self.assertNotIn(secret_message, raw)
                self.assertEqual(deleted, ["pod", "networkpolicy"])
                self.assertEqual(result["cleanup"], not cleanup_error)
                self.assertEqual(result["checks"][-1], {"name": "live_control_after", "passed": False})
                state = result["canary_state_before_cleanup"]
                self.assertTrue(state["observed"])
                self.assertFalse(state["logs_collected"])
                self.assertEqual(state["containers"][0]["state"], {"kind": "terminated", "exit_code": 1, "signal": 0})
                if cleanup_error:
                    self.assertEqual(result["cleanup_errors"], [{"resource": "pod", "error_type": "RuntimeError"}])


if __name__ == "__main__":
    unittest.main(verbosity=2)
