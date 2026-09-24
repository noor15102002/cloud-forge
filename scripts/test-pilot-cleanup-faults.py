#!/usr/bin/env python3
"""Source-level fake-runner tests; these do not execute Docker or CloudForge."""
import copy
from contextlib import redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("cleanup_faults", Path(__file__).with_name("pilot-cleanup-faults.py"))
faults = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(faults)
OWNER = "cloudforge-" + "a" * 20
TOKEN = "b" * 32
HARNESS_TOKEN = "c" * 32


def observation(states=None):
    states = states or {}
    classes = {}
    for kind in faults.RESOURCE_CLASSES:
        status = states.get(kind, "PASS")
        count = None if status == "UNKNOWN" else 1 if status == "ERROR" else 0
        classes[kind] = {"status": status, "completion_observed": status != "UNKNOWN", "possible_remnants": count,
                         "observed_identities": [{"id": "a" * 64, "name": "retained-remnant"}] if count else [],
                         "identities_omitted": 0, "reason": "fixture-observation"}
    statuses = {item["status"] for item in classes.values()}
    status = "ERROR" if "ERROR" in statuses else "UNKNOWN" if "UNKNOWN" in statuses else "PASS"
    return {"schema_version": "qualification-cleanup-v1", "status": status,
            "completion_observed": "UNKNOWN" not in statuses, "exit_code": {"PASS": 0, "ERROR": 1, "UNKNOWN": 2}[status],
            "started_at": "2026-09-22T00:00:00Z", "finished_at": "2026-09-22T00:00:01Z",
            "resource_classes": classes, "reason_codes": [], "baseline_checked": False}


class FakeDocker:
    def __init__(self):
        self.objects, self.removed, self.counter, self.templates = {}, [], 0, []

    def add(self, kind, name, labels):
        self.counter += 1
        identity = f"{self.counter:064x}" if kind == "network" else name
        self.objects[kind, identity] = {"identity": identity, "name": name, "labels": labels,
                                       "created": f"2026-09-22T00:00:{self.counter:02d}Z" if kind == "volume" else ""}
        return identity

    def run(self, arguments, timeout=20):
        kind, operation = arguments[:2]
        if operation == "create":
            labels = dict(arguments[index + 1].split("=", 1) for index, value in enumerate(arguments) if value == "--label")
            identity = self.add(kind, arguments[-1], labels)
            return subprocess.CompletedProcess(arguments, 0, (identity + "\n").encode(), b"")
        if operation == "inspect":
            reference, template = arguments[-1], arguments[3]
            self.templates.append(template)
            item = next((value for (resource_kind, identity), value in self.objects.items()
                         if resource_kind == kind and reference in (identity, value["name"])), None)
            if item is None:
                message = f"Error: No such {kind}: {reference}\n".encode()
                return subprocess.CompletedProcess(arguments, 1, b"", message)
            product = re.search(r'index .Labels "cloudforge.dev/ownership"\) "([^"]*)"', template)
            harness = re.search(r'index .Labels "cloudforge.dev/qualification"\) "([^"]*)"', template).group(1)
            if product is None:
                assert '{{json false}}' in template
            # Docker's Go template map[string]string lookup returns "" for a
            # missing label, unlike dict.get() without an explicit default.
            values = [item["identity"], item["name"], item["created"],
                      item["labels"].get(faults.PRODUCT_LABEL, "") == product.group(1) if product else False,
                      item["labels"].get(faults.HARNESS_LABEL, "") == harness]
            return subprocess.CompletedProcess(arguments, 0, (" ".join(json.dumps(value) for value in values) + "\n").encode(), b"")
        if operation == "ls":
            prefix = arguments[arguments.index("--filter") + 1].removeprefix("name=")
            values = [item for (resource_kind, _), item in self.objects.items() if resource_kind == kind and prefix in item["name"]]
            payload = "".join((item["name"] if kind == "volume" else item["identity"] + " " + item["name"]) + "\n" for item in values)
            return subprocess.CompletedProcess(arguments, 0, payload.encode(), b"")
        if operation == "rm":
            self.removed.append((kind, arguments[-1]))
            self.objects.pop((kind, arguments[-1]), None)
            return subprocess.CompletedProcess(arguments, 0, b"removed\n", b"")
        raise AssertionError("Unexpected fake Docker operation")


class CleanupFaultTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cloudforge-cleanup-fault-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.fake = FakeDocker()
        self.session = self.make_session("owned-remnant")

    def make_session(self, case):
        faults.write_json(self.root / "private-state.json", {"case": case, "harness_token": HARNESS_TOKEN,
                          "owner": OWNER, "product_token": TOKEN, "resources": {}}, private=True)
        session = faults.Session(self.root, "fake-docker")
        session.command = self.fake.run
        return session

    def provision(self, session):
        for kind in ("network", "volume"):
            name = "k3d-" + OWNER + ("-images" if kind == "volume" else "")
            arguments = [kind, "create", "--driver", "bridge", "--opt", "com.docker.network.bridge.enable_ip_masquerade=true"] if kind == "network" else [kind, "create", "--driver", "local"]
            arguments += ["--label", "app=k3d", "--label", "k3d.cluster=" + OWNER,
                          "--label", faults.PRODUCT_LABEL + "=" + TOKEN, name]
            result = session.intercept("docker", arguments)
            self.assertEqual(result.returncode, 0)

    def test_private_owner_registration_requires_exact_buildx_contract(self):
        args = ["buildx", "create", "--name", OWNER, "--driver", "docker-container", "--driver-opt",
                "memory=2g,cpu-period=100000,cpu-quota=200000,env.CLOUDFORGE_RUN_ID=" + OWNER + ",env.CLOUDFORGE_OWNER_ID=" + TOKEN]
        self.assertEqual(faults.builder_owner(args), (OWNER, TOKEN))
        for position, value in ((3, "unrelated"), (5, "docker"), (7, args[7] + ",extra=true")):
            changed = list(args)
            changed[position] = value
            self.assertIsNone(faults.builder_owner(changed))
        self.assertEqual(stat.S_IMODE((self.root / "private-state.json").stat().st_mode), 0o600)

    def test_sentinels_before_private_token_capture_keep_their_identity(self):
        for token in (None, "", "a" * 31, "g" * 32, False):
            for kind in ("network", "volume"):
                with self.subTest(token=token, kind=kind):
                    self.fake = FakeDocker()
                    session = self.make_session("owned-remnant")
                    if token is None:
                        del session.state["product_token"]
                    else:
                        session.state["product_token"] = token
                    session.create_sentinel("unrelated", kind, "unrelated-qualification-sentinel")
                    proof = session.state["resources"]["unrelated"]
                    self.assertFalse(proof["product_owned"])
                    self.assertTrue(proof["harness_owned"])
                    self.assertTrue(all('{{json false}}' in template for template in self.fake.templates))
                    self.assertTrue(all(faults.PRODUCT_LABEL not in template for template in self.fake.templates))
                    arguments = ["buildx", "create", "--name", OWNER, "--driver", "docker-container", "--driver-opt",
                                 "memory=2g,cpu-period=100000,cpu-quota=200000,env.CLOUDFORGE_RUN_ID=" + OWNER + ",env.CLOUDFORGE_OWNER_ID=" + TOKEN]
                    self.assertIsNone(session.intercept("docker", arguments))
                    self.assertEqual(session.inspect(kind, proof["identity"]), proof)
                    self.assertIn(faults.PRODUCT_LABEL, self.fake.templates[-1])
                    self.assertNotIn('{{json false}}', self.fake.templates[-1])
                    self.assertEqual(session.remove(proof), "removed_captured_identity")
                    self.assertEqual(self.fake.objects, {})

    def test_fixture_models_docker_empty_string_for_missing_label(self):
        identity = self.fake.add("network", "unrelated-sentinel", {faults.HARNESS_LABEL: HARNESS_TOKEN})
        old_template = ('{{json .Id}} {{json .Name}} {{json ""}} '
                        '{{json (eq (index .Labels "' + faults.PRODUCT_LABEL + '") "")}} '
                        '{{json (eq (index .Labels "' + faults.HARNESS_LABEL + '") "' + HARNESS_TOKEN + '")}}')
        result = self.fake.run(["network", "inspect", "--format", old_template, identity])
        self.assertTrue(faults.fields(result.stdout)[3])

    def test_owned_infrastructure_and_already_absent_are_distinct(self):
        for case in ("owned-remnant", "already-absent"):
            with self.subTest(case=case):
                self.fake = FakeDocker()
                session = self.make_session(case)
                self.provision(session)
                result = session.intercept("k3d", faults.cluster_arguments(OWNER, TOKEN))
                self.assertEqual(result.returncode, 70)
                self.assertTrue(session.state["injected"])
                self.assertEqual(len(self.fake.objects), 2 if case == "owned-remnant" else 0)
                if case == "already-absent":
                    self.assertEqual(session.remove(session.state["resources"]["invocation-network"]), "already_absent")

    def test_cluster_fault_requires_exact_private_public_fixture_contract(self):
        self.provision(self.session)
        arguments = faults.cluster_arguments(OWNER, TOKEN)
        for index, value in ((4, "unrelated-image"), (18, "cloudforge.dev/ownership=" + "d" * 32 + "@all"), (20, "0.0.0.0:0:30080@server:0")):
            changed = list(arguments)
            changed[index] = value
            with self.subTest(index=index), self.assertRaises(faults.guards.QualificationError):
                self.session.intercept("k3d", changed)
        self.assertEqual(self.fake.removed, [])

    def test_inventory_faults_require_real_complete_scoped_inventory(self):
        for case in ("inventory-malformed", "inventory-truncated"):
            with self.subTest(case=case):
                self.fake = FakeDocker()
                session = self.make_session(case)
                self.provision(session)
                args = faults.inventory_arguments("network", OWNER)
                self.assertIsNone(session.intercept("docker", args))
                session.intercept("k3d", faults.cluster_arguments(OWNER, TOKEN))
                result = session.intercept("docker", args)
                self.assertEqual(result.returncode, 0)
                self.assertTrue(session.state["injected"])
                self.assertEqual(result.stdout.endswith(b"\n"), case == "inventory-malformed")
                self.assertIsNone(session.intercept("docker", faults.inventory_arguments("network", "cloudforge-" + "d" * 20)))
                with patch.object(session, "command", return_value=subprocess.CompletedProcess(args, 0, b"partial", b"")):
                    with self.assertRaises(faults.guards.QualificationError):
                        session.intercept("docker", args)

    def test_preflight_collision_is_harness_owned_and_not_product_owned(self):
        session = self.make_session("same-name-unrelated")
        args = faults.inventory_arguments("network", OWNER)
        self.assertIsNone(session.intercept("docker", args))
        proof = session.state["resources"]["same-name-unrelated"]
        self.assertTrue(proof["harness_owned"])
        self.assertFalse(proof["product_owned"])
        self.assertTrue(self.fake.run(args).stdout)
        self.assertEqual(self.fake.removed, [])
        session.intercept("docker", args)
        self.assertEqual(len(self.fake.objects), 1)

    def test_replacement_faults_preserve_old_and_new_identity_separately(self):
        for case in ("foreign-owner", "replaced-identity"):
            with self.subTest(case=case):
                self.fake = FakeDocker()
                session = self.make_session(case)
                self.provision(session)
                original = session.state["resources"]["invocation-network"]
                session.intercept("k3d", faults.cluster_arguments(OWNER, TOKEN))
                replacement = session.state["resources"]["replacement"]
                self.assertNotEqual(original["identity"], replacement["identity"])
                self.assertEqual(replacement["product_owned"], case == "replaced-identity")
                self.assertTrue(replacement["harness_owned"])
                self.assertEqual(session.remove(original), "already_absent")
                self.assertEqual(session.inspect("network", replacement["identity"]), replacement)

    def test_teardown_refuses_changed_marker_and_same_name_volume_creation(self):
        self.provision(self.session)
        network = self.session.state["resources"]["invocation-network"]
        self.fake.objects["network", network["identity"]]["labels"] = {}
        with self.assertRaises(faults.guards.QualificationError):
            self.session.remove(network)
        volume = self.session.state["resources"]["invocation-volume"]
        self.fake.objects["volume", volume["identity"]]["created"] = "2026-09-23T00:00:00Z"
        with self.assertRaises(faults.guards.QualificationError):
            self.session.remove(volume)
        self.assertEqual(self.fake.removed, [])
        for created in ("not-a-timestamp", "2026-09-23T00:00:00"):
            self.fake.objects["volume", volume["identity"]]["created"] = created
            with self.subTest(created=created), self.assertRaises(faults.guards.QualificationError):
                self.session.inspect("volume", volume["identity"])

    def test_removal_faults_withhold_only_the_exact_registered_operation(self):
        for case in ("remove-failure", "builder-fallback"):
            with self.subTest(case=case):
                self.fake = FakeDocker()
                session = self.make_session(case)
                self.provision(session)
                session.intercept("k3d", faults.cluster_arguments(OWNER, TOKEN))
                args = ["buildx", "rm", "--force", OWNER] if case == "builder-fallback" else ["network", "rm", session.state["resources"]["invocation-network"]["identity"]]
                result = session.intercept("docker", args)
                self.assertEqual(result.returncode, 70)
                self.assertTrue(session.state["injected"])
                self.assertEqual(self.fake.removed, [])
                self.assertIsNone(session.intercept("docker", args[:-1] + ["unrelated"]))

    def test_events_never_export_private_tokens_or_creation_arguments(self):
        self.provision(self.session)
        self.session.intercept("k3d", faults.cluster_arguments(OWNER, TOKEN))
        content = (self.root / "events.jsonl").read_text()
        self.assertNotIn(TOKEN, content)
        self.assertNotIn(HARNESS_TOKEN, content)
        self.assertNotIn("driver-opt", content)
        self.assertNotIn(faults.PRODUCT_LABEL, content)

    def test_missing_is_not_empty_success_or_an_arbitrary_daemon_error(self):
        self.assertTrue(faults.missing(subprocess.CompletedProcess([], 1, b"", b"Error response from daemon: network abc not found"), "network", "abc"))
        for result in (subprocess.CompletedProcess([], 0, b"", b""), subprocess.CompletedProcess([], 1, b"", b"daemon unavailable"),
                       subprocess.CompletedProcess([], 1, b"partial", b"Error: No such network: abc")):
            self.assertFalse(faults.missing(result, "network", "abc"))
        with self.assertRaises(faults.guards.QualificationError):
            self.session.inspect("image", "unrelated")

    def test_native_cleanup_is_retained_without_rewriting_error(self):
        report = {"status": "error", "evidence": [{"experiment_id": "environment-cleanup", "status": "error"}]}
        (self.root / "stdout.json").write_text(json.dumps(report))
        self.assertEqual(faults.retain_native_cleanup(self.root), report)
        saved = json.loads((self.root / "cloudforge-cleanup.json").read_text())
        self.assertEqual(saved["result"], "ERROR")
        self.assertEqual(saved["source"], "original_native_report")
        (self.root / "stdout.json").write_text('{"status":')
        self.assertIsNone(faults.retain_native_cleanup(self.root))
        self.assertEqual(json.loads((self.root / "cloudforge-cleanup.json").read_text())["result"], "UNKNOWN")
        for status in (None, 1, [], {}):
            report["evidence"][0]["status"] = status
            (self.root / "stdout.json").write_text(json.dumps(report))
            faults.retain_native_cleanup(self.root)
            self.assertEqual(json.loads((self.root / "cloudforge-cleanup.json").read_text())["result"], "UNKNOWN")

    def test_duplicate_unusable_native_records_remain_unknown(self):
        cleanup = {"experiment_id": "environment-cleanup", "status": "pass"}
        payloads = [b"", b"null", b"[]", b"{}", b'{"status":"error","evidence":[',
                    b'{"status":"error","status":"pass","evidence":[]}',
                    b'{"status":"error","evidence":[{"experiment_id":"environment-cleanup","status":"error","status":"pass"}]}',
                    b'{"status":"error","evidence":[],"number":NaN}',
                    json.dumps({"status": "error", "evidence": [cleanup, cleanup]}).encode(),
                    json.dumps({"status": "error", "evidence": [cleanup, None]}).encode(),
                    json.dumps({"status": "error", "evidence": []}).encode()]
        for payload in payloads:
            with self.subTest(payload=payload):
                (self.root / "stdout.json").write_bytes(payload)
                self.assertIsNone(faults.retain_native_cleanup(self.root))
                self.assertEqual(json.loads((self.root / "cloudforge-cleanup.json").read_text())["result"], "UNKNOWN")
                self.assertEqual((self.root / "stdout.json").read_bytes(), payload)

    def test_independent_observer_preserves_structured_unknown_and_positive_remnants(self):
        for states in ({}, {"network": "ERROR"}, {"network": "UNKNOWN"}, {"network": "ERROR", "volume": "UNKNOWN"}):
            record = observation(states)
            result = subprocess.CompletedProcess([], record["exit_code"], json.dumps(record).encode(), b"original stderr")
            with self.subTest(states=states), patch.object(faults.guards, "capture", return_value=result):
                observed = faults.independent_check(self.root, "test")
                self.assertEqual(observed, record)
                self.assertEqual(observed["resource_classes"], record["resource_classes"])
                self.assertEqual(faults.trustworthy_observation(observed), "UNKNOWN" not in states.values())
                self.assertEqual((self.root / "independent-test.stdout.txt").read_bytes(), result.stdout)
                self.assertEqual((self.root / "independent-test.stderr.txt").read_bytes(), result.stderr)

    def test_consumer_accepts_actual_checker_records_from_fake_inventories(self):
        spec = importlib.util.spec_from_file_location("independent_cleanup_for_test", Path(__file__).with_name("pilot-cleanup-check.py"))
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
        for payload, expected in (("", "PASS"), ("a" * 64 + " k3d-cloudforge-abc\n", "ERROR"), ("truncated", "UNKNOWN")):
            def run(arguments, **_kwargs):
                return subprocess.CompletedProcess(arguments, 0, payload if arguments[1] == "network" else "", "")
            classes = checker.observe_resources(runner=run)
            stream = io.StringIO()
            with patch.object(checker, "observe_resources", return_value=classes), redirect_stdout(stream):
                code = checker.main([])
            record = faults.strict_json(stream.getvalue())
            with self.subTest(expected=expected):
                self.assertEqual(faults.validate_observation(record, code), record)
                self.assertEqual(record["status"], expected)
                self.assertEqual(faults.trustworthy_observation(record), expected != "UNKNOWN")

    def test_empty_partial_and_unavailable_observer_output_never_proves_absence(self):
        for payload, code in ((b"", 0), (b"", 2), (b"[]", 0), (b"{}", 0), (b'{"status":', 0),
                              (b'{"status":"ERROR","status":"PASS"}', 0)):
            with self.subTest(payload=payload, code=code), patch.object(faults.guards, "capture", return_value=subprocess.CompletedProcess([], code, payload, b"")):
                value = faults.independent_check(self.root, "test")
                self.assertEqual(value["status"], "UNKNOWN")
                self.assertFalse(value["completion_observed"])
                self.assertFalse(faults.trustworthy_observation(value))
        with patch.object(faults.guards, "capture", side_effect=faults.guards.QualificationError("unavailable")):
            value = faults.independent_check(self.root, "unavailable")
            self.assertEqual(value["status"], "UNKNOWN")
            self.assertIsNone(value["exit_code"])
            self.assertFalse(value["process_completion_observed"])

    def test_inconsistent_observer_status_exit_and_completeness_are_rejected(self):
        cases = []
        for states, code in (({}, 1), ({"network": "ERROR"}, 0), ({"network": "UNKNOWN"}, 1)):
            cases.append((observation(states), code))
        for mutation in ("missing-class", "empty-classes", "false-completion", "bad-count", "bad-class-completion", "missing-identities"):
            value = observation()
            if mutation == "missing-class": del value["resource_classes"]["volume"]
            if mutation == "empty-classes": value["resource_classes"] = {}
            if mutation == "false-completion": value["completion_observed"] = False
            if mutation == "bad-count": value["resource_classes"]["network"]["possible_remnants"] = 1
            if mutation == "bad-class-completion": value["resource_classes"]["network"]["completion_observed"] = False
            if mutation == "missing-identities": del value["resource_classes"]["network"]["observed_identities"]
            cases.append((value, 0))
        for original, code in cases:
            with self.subTest(original=original, code=code), patch.object(faults.guards, "capture", return_value=subprocess.CompletedProcess([], code, json.dumps(original).encode(), b"")):
                value = faults.independent_check(self.root, "test")
                self.assertEqual(value["status"], "UNKNOWN")
                self.assertEqual(value["reported_record"], original)
                self.assertFalse(faults.trustworthy_observation(value))

    def test_case_requires_trustworthy_before_and_final_absence(self):
        for before in (observation(), observation({"network": "ERROR"})):
            faults.qualify_observations(before, observation())
        for before in (observation({"network": "UNKNOWN"}), observation({"network": "ERROR", "volume": "UNKNOWN"}), {}):
            with self.subTest(before=before), self.assertRaisesRegex(AssertionError, "Pre-intervention"):
                faults.qualify_observations(before, observation())
        for after in (observation({"network": "ERROR"}), observation({"network": "UNKNOWN"}), {}):
            with self.subTest(after=after), self.assertRaisesRegex(AssertionError, "Final independent"):
                faults.qualify_observations(observation(), after)

    def test_unusable_native_output_still_records_observation_and_scoped_teardown(self):
        output = self.root / "result"
        phases = []

        def native(command, report_path, **_kwargs):
            report_path.write_text('{"status":')
            report_path.with_suffix(".stderr.txt").write_text("original native failure\n")
            report_path.with_suffix(".exit.json").write_text('{"exit_code":7,"completion_observed":true}')
            return subprocess.CompletedProcess(command, 7, '{"status":', "original native failure\n")

        def observe(directory, phase):
            self.assertEqual(json.loads((directory / "cloudforge-cleanup.json").read_text())["result"], "UNKNOWN")
            phases.append(phase)
            return {"status": "PASS", "exit_code": 0, "completion_observed": True}

        with (patch.object(faults.guards, "require_runner"), patch.object(faults.shutil, "which", return_value="fake-docker"),
              patch.object(faults.Session, "command", new=lambda _session, arguments, timeout=20: self.fake.run(arguments, timeout)),
              patch.object(faults, "run_observed", side_effect=native), patch.object(faults, "independent_check", side_effect=observe),
              patch.object(sys, "argv", ["pilot-cleanup-faults.py", "/fake/binary", str(output), "--case", "owned-remnant"]),
              self.assertRaisesRegex(AssertionError, "Native report unusable")):
            faults.main()
        self.assertEqual(phases, ["before-intervention", "after-intervention"])
        self.assertEqual((output / "stdout.json").read_text(), '{"status":')
        self.assertEqual(json.loads((output / "stdout.exit.json").read_text())["exit_code"], 7)
        self.assertEqual(json.loads((output / "cloudforge-cleanup.json").read_text())["result"], "UNKNOWN")
        self.assertTrue(json.loads((output / "observer-before-intervention.json").read_text())["resources"])
        self.assertTrue(all(item["outcome"] == "removed_captured_identity" for item in json.loads((output / "harness-teardown.json").read_text())["actions"]))
        self.assertEqual(self.fake.objects, {})

    def test_unknown_before_observation_cannot_be_replaced_by_final_absence(self):
        output = self.root / "before-unknown"
        native_report = {"status": "error", "evidence": [{"experiment_id": "environment-cleanup", "status": "pass"}]}

        def native(command, report_path, *, env, **_kwargs):
            report_path.write_text(json.dumps(native_report))
            report_path.with_suffix(".exit.json").write_text('{"exit_code":2,"completion_observed":true}')
            private = Path(env["CF_CLEANUP_FAULT_ROOT"]) / "private-state.json"
            state = json.loads(private.read_text())
            state["injected"] = True
            faults.write_json(private, state, private=True)
            return subprocess.CompletedProcess(command, 2, json.dumps(native_report), "")

        def observe(_directory, phase):
            return observation({"network": "UNKNOWN"}) if phase == "before-intervention" else observation()

        with (patch.object(faults.guards, "require_runner"), patch.object(faults.shutil, "which", return_value="fake-docker"),
              patch.object(faults.Session, "command", new=lambda _session, arguments, timeout=20: self.fake.run(arguments, timeout)),
              patch.object(faults, "run_observed", side_effect=native), patch.object(faults, "independent_check", side_effect=observe),
              patch.object(sys, "argv", ["pilot-cleanup-faults.py", "/fake/binary", str(output), "--case", "owned-remnant"]),
              self.assertRaisesRegex(AssertionError, "Pre-intervention independent inventory")):
            faults.main()
        self.assertEqual(json.loads((output / "cloudforge-cleanup.json").read_text())["result"], "PASS")
        self.assertEqual(json.loads((output / "observer-before-intervention.json").read_text())["independent_inventory"]["status"], "UNKNOWN")
        self.assertEqual(json.loads((output / "observer-after-intervention.json").read_text())["status"], "PASS")
        self.assertTrue(all(item["outcome"] == "removed_captured_identity" for item in json.loads((output / "harness-teardown.json").read_text())["actions"]))
        self.assertEqual(self.fake.objects, {})

    def test_qualification_requires_each_independent_layer(self):
        before = {role: {"captured": {"identity": role}, "current": {"identity": role}} for role in ("unrelated-network", "unrelated-volume", "replacement", "same-name-unrelated")}
        before.update({"invocation-" + kind: {"captured": {}, "current": None} for kind in ("network", "volume")})
        events = [{"operation": name, "exit_code": 0} for name in ("native_builder_fallback_container", "native_builder_fallback_volume")]
        for case in faults.CASES:
            report = {"status": "error", "evidence": [{"experiment_id": "container-build", "status": "pass", "execution": {"executed": True}},
                      {"experiment_id": "container-scan", "status": "warn", "execution": {"executed": True}}, {"experiment_id": "environment-cleanup", "status": "pass" if case in faults.PASS_CLEANUP else "error"}],
                      "diagnostics": [{"code": code} for code in ("cluster_create_failed", "resource_ownership_unavailable", "cluster_remnant_cleanup_failed", "builder_cleanup_failed")]}
            faults.qualify(report, 2, case, before, events)
            for mutation in ("native-status", "native-exit", "lost-build", "lost-scan", "cleanup", "original-error", "sentinel"):
                value, observed = copy.deepcopy(report), copy.deepcopy(before)
                if mutation == "native-status": value["status"] = "pass"
                if mutation == "lost-build": value["evidence"][0]["status"] = "error"
                if mutation == "lost-scan": value["evidence"][1]["status"] = "error"
                if mutation == "cleanup": value["evidence"][2]["status"] = "error" if case in faults.PASS_CLEANUP else "pass"
                if mutation == "original-error": value["diagnostics"] = []
                if mutation == "sentinel": observed["unrelated-network"]["current"] = None
                with self.subTest(case=case, mutation=mutation), self.assertRaises(AssertionError):
                    faults.qualify(value, 0 if mutation == "native-exit" else 2, case, observed, events)

    def test_runtime_entry_refuses_local_execution(self):
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(faults.guards.QualificationError):
            faults.tool_wrapper("docker", ["version"])


if __name__ == "__main__":
    unittest.main()
