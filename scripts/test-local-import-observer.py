#!/usr/bin/env python3
"""Offline proof of public image → owned node → private archive → exact local import."""
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import local_import_observer as observer
from reliability_import_observer import helpers

RUN = "cloudforge-" + "a" * 20
TOKEN = "b" * 32
NODE = "c" * 64
IMAGE = "cloudforge/healthy-node-api:" + "a" * 20 + "-a"
CREATE = ["cluster", "create", RUN, "--runtime-label", "cloudforge.dev/owned=true@all",
          "--runtime-label", "cloudforge.dev/run-id=" + RUN + "@all", "--runtime-label", "cloudforge.dev/ownership=" + TOKEN + "@all"]


class LocalImportTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.state = self.root / "local-import.json"
        self.workspace = self.root / "cloudforge-verify-123"
        self.workspace.mkdir(mode=0o700)
        self.tmpdir = self.workspace / "subprocess-tmp"
        self.tmpdir.mkdir()
        self.archive = self.workspace / "cloudforge-import-123" / "image.tar"
        self.archive.parent.mkdir(mode=0o700)
        self.archive.write_bytes(b"BINARY-ARCHIVE-CANARY\0\xff")
        self.archive.chmod(0o600)
        self.node_archive = "/tmp/" + self.archive.parent.name + "/image.tar"
        self.stdout, self.stderr = io.BytesIO(), io.BytesIO()
        self.environment = patch.dict(os.environ, TMPDIR=str(self.tmpdir))
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.calls = []

    def observe(self, tool, arguments):
        return observer.observe(tool, arguments, self.state, RUN, ["healthy-node-api"], "/unused/docker",
                                helpers, self.stdout, self.stderr)

    def node_observation(self, values=None, *, raw=None, exit_code=0, truncated=False):
        values = values if values is not None else {"id": NODE, **{name: True for name in ("name", "owner", "run", "cluster", "role", "running")}}
        payload = raw if raw is not None else json.dumps(values).encode()
        def tee(command, directory, _stage, stdout, stderr, **kwargs):
            self.calls.append(command)
            self.assertEqual(kwargs["name"], "node-ownership")
            stdout.write(payload)
            stderr.write(b"original-node-stderr")
            (directory / "node.stdout").write_bytes(payload)
            (directory / "node.json").write_text(json.dumps({"completion_observed": True,
                "streams": {"stdout": {"file": "node.stdout", "truncated": truncated}}}))
            return exit_code
        return patch.object(helpers, "tee_command", side_effect=tee)

    def register(self):
        self.assertEqual(self.observe("k3d", CREATE), (False, None))
        arguments = ["container", "inspect", "--format", observer.node_template(RUN, TOKEN), "k3d-" + RUN + "-server-0"]
        with self.node_observation():
            self.assertEqual(self.observe("docker", arguments), (True, 0))
        return arguments

    def bind_archive(self):
        self.assertEqual(self.observe("docker", ["image", "save", IMAGE]), (False, None))
        self.assertEqual(self.observe("docker", ["cp", str(self.archive), NODE + ":" + self.node_archive]), (False, None))
        return ["exec", NODE, *observer.CTR, self.node_archive]

    def test_complete_chain_captures_only_final_text_import_and_never_reads_archive(self):
        self.register()
        args = self.bind_archive()
        state = observer.read_state(self.state)
        self.assertEqual(state["image"], IMAGE)
        observer.validate_import(args, state)
        self.assertEqual(self.observe("docker", args), (False, None))
        self.assertEqual(len(self.calls), 1, "observer issued extra runtime commands")
        self.assertNotIn(b"BINARY-ARCHIVE-CANARY", self.stdout.getvalue())
        self.assertFalse(list(self.root.glob("private-node-observation-*")))
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o600)
        self.assertNotIn(TOKEN.encode(), self.stdout.getvalue() + self.stderr.getvalue())

    def test_node_registration_requires_every_ownership_boolean_and_exact_template(self):
        for field in ("name", "owner", "run", "cluster", "role", "running"):
            self.observe("k3d", CREATE)
            values = {"id": NODE, **{name: True for name in ("name", "owner", "run", "cluster", "role", "running")}}
            values[field] = False
            args = ["container", "inspect", "--format", observer.node_template(RUN, TOKEN), "k3d-" + RUN + "-server-0"]
            with self.subTest(field=field), self.node_observation(values):
                self.assertEqual(self.observe("docker", args), (True, 0))
                self.assertNotIn("node", observer.read_state(self.state))
                with self.assertRaises(ValueError):
                    self.observe("docker", ["image", "save", IMAGE])
        with self.node_observation():
            self.assertEqual(self.observe("docker", ["container", "inspect", "--format", "{{json .}}", "k3d-" + RUN + "-server-0"]), (False, None))
        self.assertNotIn("node", observer.read_state(self.state))

    def test_incomplete_malformed_duplicate_or_failed_node_observation_keeps_native_result(self):
        for options in ({"raw": b"partial"}, {"raw": b'{"id":"x","id":"y"}'}, {"truncated": True}, {"exit_code": 17}):
            self.observe("k3d", CREATE)
            with self.subTest(options=options), self.node_observation(**options):
                code = self.observe("docker", ["container", "inspect", "--format", observer.node_template(RUN, TOKEN), "k3d-" + RUN + "-server-0"])[1]
                self.assertEqual(code, options.get("exit_code", 0))
                self.assertNotIn("node", observer.read_state(self.state))

    def test_foreign_images_and_unregistered_local_import_are_rejected(self):
        self.register()
        for image in ("private/secret:latest", IMAGE + "extra", IMAGE.replace("a" * 20, "d" * 20)):
            with self.assertRaises(ValueError):
                self.observe("docker", ["image", "save", image])
        with self.assertRaises(ValueError):
            self.observe("docker", ["exec", NODE, *observer.CTR, self.node_archive])

    def test_copy_must_bind_exact_regular_archive_to_observed_node(self):
        self.register()
        self.observe("docker", ["image", "save", IMAGE])
        variants = [(str(self.archive), "d" * 64 + ":" + self.node_archive),
                    (str(self.archive), NODE + ":/tmp/foreign/image.tar"),
                    (str(self.root / "image.tar"), NODE + ":" + self.node_archive)]
        for source, target in variants:
            with self.assertRaises(ValueError):
                self.observe("docker", ["cp", source, target])
        self.archive.unlink()
        self.archive.symlink_to(self.state)
        with self.assertRaises(ValueError):
            self.observe("docker", ["cp", str(self.archive), NODE + ":" + self.node_archive])
        self.archive.unlink()
        os.mkfifo(self.archive)
        with self.assertRaises(ValueError):
            self.observe("docker", ["cp", str(self.archive), NODE + ":" + self.node_archive])

    def test_local_import_cannot_add_flags_switch_namespace_path_or_node(self):
        self.register()
        args = self.bind_archive()
        variants = [[*args, "--untrusted"], ["exec", "d" * 64, *args[2:]],
                    [arg.replace("k8s.io", "private") for arg in args],
                    [*args[:-1], "/tmp/cloudforge-import-456/image.tar"],
                    [arg for arg in args if arg != "--local"]]
        for variant in variants:
            with self.subTest(arguments=variant), self.assertRaises(ValueError):
                self.observe("docker", variant)

    def test_image_b_requires_new_export_copy_binding(self):
        self.register()
        args = self.bind_archive()
        self.observe("docker", ["image", "save", IMAGE[:-1] + "b"])
        with self.assertRaises(ValueError):
            self.observe("docker", args)
        self.observe("docker", ["cp", str(self.archive), NODE + ":" + self.node_archive])
        observer.validate_import(args, observer.read_state(self.state))
        self.assertEqual(observer.read_state(self.state)["image"], IMAGE[:-1] + "b")

    def test_unrelated_commands_pass_through_without_capture_or_registration(self):
        for tool, args in (("k3d", ["kubeconfig", "get", RUN]), ("docker", ["logs", "unrelated"]),
                           ("docker", ["exec", NODE, "crictl", "inspecti", "--quiet", IMAGE])):
            self.assertEqual(self.observe(tool, args), (False, None))
        self.assertFalse(self.state.exists())
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
