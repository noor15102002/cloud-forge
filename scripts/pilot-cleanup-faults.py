#!/usr/bin/env python3
"""Qualify cleanup through an installed binary on disposable GitHub runners.

The copied public fixture builds normally. A declared k3d-create failure reaches
cleanup after real private network/volume provisioning, without starting a
cluster. Faults affect only that invocation. Native cleanup, the independent
observation before intervention, and scoped harness teardown remain separate.

already-absent exercises the public CLI's idempotent absence boundary. Invoking
the same internal cleanup method twice is source-level proof, not this harness.
"""
import argparse
import datetime
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
import time

from qualification_command import run_observed
from qualification_record import reject_constant, unique_object

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("cleanup_public_guards", ROOT / "scripts/pilot-backend-cancellation.py")
guards = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guards)
CASES = ("owned-remnant", "already-absent", "inventory-malformed", "inventory-truncated",
         "remove-failure", "same-name-unrelated", "foreign-owner", "replaced-identity", "builder-fallback")
PASS_CLEANUP = {"owned-remnant", "already-absent", "same-name-unrelated"}
RUN = re.compile(r"cloudforge-(?:[a-f0-9]{8}|[a-f0-9]{20}|[a-f0-9]{32})")
IDENTITY = re.compile(r"[a-f0-9]{64}")
PRODUCT_LABEL = "cloudforge.dev/ownership"
HARNESS_LABEL = "cloudforge.dev/qualification"
FAULT_EXIT = 70
RESOURCE_CLASSES = {"container", "network", "volume", "image", "owned_image", "builder"}


def strict_json(payload):
    return json.loads(payload, object_pairs_hook=unique_object, parse_constant=reject_constant)


def write_json(path, value, private=False):
    pending = path.with_name(path.name + ".pending")
    descriptor = os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")
    pending.replace(path)
    if private:
        path.chmod(0o600)


def fields(payload):
    decoder, values, remaining = json.JSONDecoder(), [], payload.decode().strip()
    while remaining:
        value, end = decoder.raw_decode(remaining)
        values.append(value)
        remaining = remaining[end:].lstrip()
    return values


def inventory_arguments(kind, owner):
    args = [kind, "ls"]
    if kind == "container":
        args.append("--all")
    if kind != "volume":
        args.append("--no-trunc")
    format_string = "{{.Name}}" if kind == "volume" else "{{.ID}} {{.Names}}" if kind == "container" else "{{.ID}} {{.Name}}"
    return args + ["--filter", "name=k3d-" + owner, "--format", format_string]


def builder_owner(arguments):
    if len(arguments) != 8 or arguments[:3] != ["buildx", "create", "--name"] or not RUN.fullmatch(arguments[3]):
        return None
    prefix = "memory=2g,cpu-period=100000,cpu-quota=200000,env.CLOUDFORGE_RUN_ID=" + arguments[3] + ",env.CLOUDFORGE_OWNER_ID="
    if arguments[4:7] != ["--driver", "docker-container", "--driver-opt"] or not arguments[7].startswith(prefix):
        return None
    token = arguments[7][len(prefix):]
    if not re.fullmatch(r"[a-f0-9]{32}", token):
        return None
    return arguments[3], token


def cluster_arguments(owner, token):
    return ["cluster", "create", owner, "--image", "rancher/k3s:v1.35.5-k3s1", "--servers-memory", "4g",
            "--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--runtime-label",
            "cloudforge.dev/owned=true@all", "--runtime-label", "cloudforge.dev/run-id=" + owner + "@all",
            "--servers", "1", "--agents", "0", "--runtime-label", PRODUCT_LABEL + "=" + token + "@all",
            "--port", "127.0.0.1:0:30080@server:0", "--wait", "--timeout", "90s"]


def missing(result, kind, reference):
    if result.returncode != 1 or result.stdout.strip():
        return False
    message = result.stderr.decode(errors="replace").strip()
    values = {"Error: No such " + kind + ": " + reference,
              "Error response from daemon: No such " + kind + ": " + reference,
              "Error: No such object: " + reference}
    if kind == "network":
        values.add("Error response from daemon: network " + reference + " not found")
    if kind == "volume":
        values.add("Error response from daemon: get " + reference + ": no such volume")
    return message in values


class Session:
    def __init__(self, root, docker):
        self.root, self.docker = root, docker
        self.state = json.loads((root / "private-state.json").read_text())

    def save(self):
        write_json(self.root / "private-state.json", self.state, private=True)

    def event(self, operation, **data):
        # Callers supply fixed fields, public IDs, booleans and return codes;
        # never command arguments, environment arrays, labels or private state.
        with (self.root / "events.jsonl").open("a") as stream:
            stream.write(json.dumps({"case": self.state["case"], "operation": operation, **data}) + "\n")

    def command(self, args, timeout=20):
        return guards.capture([self.docker, *args], timeout, limit=32 * 1024)

    def inspect(self, kind, reference):
        if kind not in ("network", "volume"):
            raise guards.QualificationError("harness_resource_class_not_supported")
        product, harness = self.state.get("product_token", ""), self.state["harness_token"]
        identity = ".Name" if kind == "volume" else ".Id"
        created = ".CreatedAt" if kind == "volume" else '""'
        # Go templates return an empty string for a missing map[string]string
        # label. An unknown token must never match that absent ownership label.
        product_owned = '{{json false}}'
        if isinstance(product, str) and re.fullmatch(r"[a-f0-9]{32}", product):
            product_owned = '{{json (eq (index .Labels "' + PRODUCT_LABEL + '") "' + product + '")}}'
        template = ('{{json ' + identity + '}} {{json .Name}} {{json ' + created + '}} '
                    + product_owned + ' '
                    '{{json (eq (index .Labels "' + HARNESS_LABEL + '") "' + harness + '")}}')
        result = self.command([kind, "inspect", "--format", template, reference], timeout=10)
        if missing(result, kind, reference):
            return None
        if result.returncode != 0:
            raise guards.QualificationError("cleanup_observer_unavailable")
        values = fields(result.stdout)
        if (len(values) != 5 or not all(isinstance(values[index], str) for index in (0, 1, 2))
                or not all(type(values[index]) is bool for index in (3, 4))
                or reference not in (values[0], values[1])
                or (kind == "network" and not IDENTITY.fullmatch(values[0]))
                or (kind == "volume" and (values[0] != values[1] or not values[2]))):
            raise guards.QualificationError("cleanup_observer_invalid_identity")
        if kind == "volume":
            try:
                if datetime.datetime.fromisoformat(values[2].replace("Z", "+00:00")).tzinfo is None:
                    raise ValueError("missing creation timezone")
            except ValueError as error:
                raise guards.QualificationError("cleanup_observer_invalid_creation_identity") from error
        return dict(kind=kind, identity=values[0], name=values[1], created=values[2],
                    product_owned=values[3], harness_owned=values[4])

    def remember(self, role, proof):
        if proof is None:
            raise guards.QualificationError("cleanup_capture_missing_resource")
        self.state["resources"][role] = proof
        self.save()

    def create_sentinel(self, role, kind, name, product=False):
        if self.inspect(kind, name) is not None:
            raise guards.QualificationError("sentinel_name_was_not_absent")
        args = [kind, "create", "--label", HARNESS_LABEL + "=" + self.state["harness_token"]]
        if product:
            args += ["--label", PRODUCT_LABEL + "=" + self.state["product_token"], "--label", "app=k3d",
                     "--label", "k3d.cluster=" + self.state["owner"]]
        result = self.command(args + [name])
        if result.returncode != 0:
            raise guards.QualificationError("sentinel_creation_failed")
        proof = self.inspect(kind, name)
        if (proof is None or not proof["harness_owned"] or (product and not proof["product_owned"])
                or result.stdout != (proof["identity"] + "\n").encode()):
            raise guards.QualificationError("sentinel_marker_unavailable")
        self.remember(role, proof)

    def remove(self, proof):
        current = self.inspect(proof["kind"], proof["identity"])
        if current is None:
            return "already_absent"
        if current != proof or not (current["product_owned"] or current["harness_owned"]):
            raise guards.QualificationError("harness_refused_changed_or_unowned_identity")
        result = self.command([proof["kind"], "rm", proof["identity"]])
        if result.returncode != 0 or self.inspect(proof["kind"], proof["identity"]) is not None:
            raise guards.QualificationError("harness_removal_not_established")
        return "removed_captured_identity"

    def observe(self):
        return {role: {"captured": proof, "current": self.inspect(proof["kind"], proof["identity"])}
                for role, proof in self.state["resources"].items()}

    def intercept(self, tool, arguments):
        owner = self.state.get("owner")
        created = builder_owner(arguments) if tool == "docker" else None
        if created:
            if owner not in (None, created[0]):
                raise guards.QualificationError("fault_harness_multiple_run_names")
            self.state.update(owner=created[0], product_token=created[1])
            self.save()
            return None
        if not owner:
            return None
        case, name = self.state["case"], "k3d-" + owner
        if (tool == "docker" and arguments == inventory_arguments("network", owner)
                and case == "same-name-unrelated" and not self.state.get("injected")):
            self.create_sentinel("same-name-unrelated", "network", name)
            self.state["injected"] = True
            self.save()
            self.event("created_same_name_before_preflight", actual_command_executed=True)
        for kind in ("network", "volume"):
            expected = [kind, "create", "--driver", "bridge", "--opt", "com.docker.network.bridge.enable_ip_masquerade=true"] if kind == "network" else [kind, "create", "--driver", "local"]
            expected += ["--label", "app=k3d", "--label", "k3d.cluster=" + owner, "--label", PRODUCT_LABEL + "=" + self.state["product_token"], name + ("-images" if kind == "volume" else "")]
            if tool == "docker" and arguments == expected:
                result = self.command(arguments, timeout=30)
                if result.returncode == 0:
                    proof = self.inspect(kind, expected[-1])
                    if proof is None or not proof["product_owned"]:
                        raise guards.QualificationError("provisioned_resource_marker_unavailable")
                    self.remember("invocation-" + kind, proof)
                    self.event("provisioned_" + kind, identity=proof["identity"], actual_command_executed=True)
                return result
        if tool == "k3d" and arguments[:3] == ["cluster", "create", owner]:
            # Never create a real cluster for this fault matrix. All other
            # k3d argument shapes pass through; ownership comes from provision.
            if arguments != cluster_arguments(owner, self.state["product_token"]):
                raise guards.QualificationError("fault_requires_exact_public_cluster_contract")
            if not all("invocation-" + kind in self.state["resources"] for kind in ("network", "volume")):
                raise guards.QualificationError("fault_requires_captured_private_infrastructure")
            self.state["cleanup_phase"] = True
            if case == "already-absent":
                for kind in ("volume", "network"):
                    self.remove(self.state["resources"]["invocation-" + kind])
                self.state["injected"] = True
                self.event("removed_before_native_cleanup", actual_command_executed=True)
            elif case in ("foreign-owner", "replaced-identity"):
                original = self.state["resources"]["invocation-network"]
                self.remove(original)
                self.create_sentinel("replacement", "network", name, product=case == "replaced-identity")
                if self.state["resources"]["replacement"]["identity"] == original["identity"]:
                    raise guards.QualificationError("replacement_did_not_change_identity")
                self.state["injected"] = True
                self.event("replaced_captured_network", same_product_marker=case == "replaced-identity", actual_command_executed=True)
            elif case == "owned-remnant":
                self.state["injected"] = True
            self.save()
            self.event("withheld_cluster_create", actual_command_executed=False, exit_code=FAULT_EXIT)
            return subprocess.CompletedProcess(arguments, FAULT_EXIT, b"", b"Declared public cleanup qualification cluster-create failure.\n")
        if not self.state.get("cleanup_phase"):
            return None
        if (tool == "docker" and arguments == inventory_arguments("network", owner)
                and case in ("inventory-malformed", "inventory-truncated")):
            original = self.command(arguments)
            if original.returncode != 0 or not original.stdout.endswith(b"\n"):
                raise guards.QualificationError("fault_requires_complete_real_inventory")
            payload = original.stdout[:-1] if case == "inventory-truncated" else b"not-an-identity " + name.encode() + b"\n"
            self.state["injected"] = True
            self.save()
            self.event("altered_inventory", actual_command_executed=True, original_exit_code=original.returncode,
                       original_stdout=original.stdout.decode(), supplied_stdout=payload.decode())
            return subprocess.CompletedProcess(arguments, 0, payload, original.stderr)
        if (tool == "docker" and case == "remove-failure"
                and arguments == ["network", "rm", self.state["resources"]["invocation-network"]["identity"]]):
            self.state["injected"] = True
            self.save()
            self.event("withheld_network_removal", actual_command_executed=False, exit_code=FAULT_EXIT)
            return subprocess.CompletedProcess(arguments, FAULT_EXIT, b"", b"Declared public cleanup qualification removal failure.\n")
        if tool == "docker" and arguments == ["buildx", "rm", "--force", owner] and case == "builder-fallback":
            self.state["injected"] = True
            self.save()
            self.event("withheld_builder_removal", actual_command_executed=False, exit_code=FAULT_EXIT)
            return subprocess.CompletedProcess(arguments, FAULT_EXIT, b"", b"Declared public cleanup qualification builder-removal failure.\n")
        if (tool == "docker" and len(arguments) == 4 and arguments[:3] == ["container", "rm", "--force"]
                and IDENTITY.fullmatch(arguments[3]) and case == "builder-fallback"):
            # Observation only: the product's captured identity selects removal.
            result = self.command(arguments)
            self.event("native_builder_fallback_container", actual_command_executed=True, exit_code=result.returncode)
            return result
        if tool == "docker" and arguments == ["volume", "rm", "buildx_buildkit_" + owner + "0_state"] and case == "builder-fallback":
            result = self.command(arguments)
            self.event("native_builder_fallback_volume", actual_command_executed=True, exit_code=result.returncode)
            return result
        return None


def tool_wrapper(tool, arguments):
    guards.require_runner()
    root = Path(os.environ["CF_CLEANUP_FAULT_ROOT"])
    if root.is_symlink() or root.resolve() != root or not root.name.startswith("cloudforge-cleanup-fault-") or tool not in ("docker", "k3d"):
        raise guards.QualificationError("fault_requires_owned_public_fixture")
    session = Session(root, os.environ["CF_CLEANUP_REAL_DOCKER"])
    for path in (root / "app", *(root / "app").rglob("*")):
        mode = path.lstat().st_mode
        if not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
            raise guards.QualificationError("fault_public_fixture_nonregular_file")
    if session.state["case"] not in CASES or guards.hashes(root / "app") != session.state["source_hashes"]:
        raise guards.QualificationError("fault_public_fixture_changed")
    result = session.intercept(tool, arguments)
    if result is None:
        real = os.environ["CF_CLEANUP_REAL_" + tool.upper()]
        os.execv(real, [real, *arguments])
    sys.stdout.buffer.write(result.stdout)
    sys.stderr.buffer.write(result.stderr)
    return result.returncode


def qualify(report, code, case, before, events):
    evidence = {item["experiment_id"]: item for item in report["evidence"]}
    assert report["status"] == "error" and code == 2, "Native operational ERROR/exit2 was not retained"
    assert evidence["container-build"]["status"] == "pass", "Original build PASS was lost"
    assert evidence["container-scan"]["status"] in ("pass", "warn"), "Original scanner evidence was lost"
    assert all(evidence[key].get("execution", {}).get("executed") is True for key in ("container-build", "container-scan")), "Earlier executed work was lost"
    expected = "pass" if case in PASS_CLEANUP else "error"
    assert evidence["environment-cleanup"]["status"] == expected, "Native cleanup classification changed"
    codes = {item["code"] for item in report["diagnostics"]}
    assert ("resource_ownership_unavailable" if case == "same-name-unrelated" else "cluster_create_failed") in codes, "Original triggering error was lost"
    if expected == "error":
        assert ("builder_cleanup_failed" if case == "builder-fallback" else "cluster_remnant_cleanup_failed") in codes, "Cleanup diagnostic missing"
    assert not any(item.get("execution", {}).get("executed") for key, item in evidence.items()
                   if key not in ("container-build", "container-scan", "environment-cleanup")), "Unexpected deployment or experiment execution"
    for role in ("unrelated-network", "unrelated-volume"):
        assert before[role]["current"] == before[role]["captured"], "Unrelated sentinel changed"
    if case in ("same-name-unrelated", "foreign-owner", "replaced-identity"):
        role = "same-name-unrelated" if case == "same-name-unrelated" else "replacement"
        assert before[role]["current"] == before[role]["captured"], "Foreign/replaced resource was removed or changed"
    if case in ("owned-remnant", "already-absent", "builder-fallback"):
        assert all(before["invocation-" + kind]["current"] is None for kind in ("network", "volume")), "Invocation infrastructure remained"
    if case == "builder-fallback":
        assert all(any(e["operation"] == operation and e.get("exit_code") == 0 for e in events)
                   for operation in ("native_builder_fallback_container", "native_builder_fallback_volume")), "Builder fallback was not actually observed"


def validate_observation(value, exit_code):
    if (not isinstance(value, dict) or value.get("schema_version") != "qualification-cleanup-v1"
            or type(exit_code) is not int or type(value.get("exit_code")) is not int
            or value["exit_code"] != exit_code or value.get("status") not in ("PASS", "ERROR", "UNKNOWN")
            or {"PASS": 0, "ERROR": 1, "UNKNOWN": 2}[value["status"]] != exit_code
            or not isinstance(value.get("started_at"), str) or not value["started_at"]
            or not isinstance(value.get("finished_at"), str) or not value["finished_at"]):
        raise ValueError("independent cleanup status or exit identity inconsistent")
    classes = value.get("resource_classes")
    if not isinstance(classes, dict) or not RESOURCE_CLASSES.issubset(classes):
        raise ValueError("independent cleanup resource classes incomplete")
    for kind, item in classes.items():
        if (not isinstance(item, dict) or item.get("status") not in ("PASS", "ERROR", "UNKNOWN")
                or item.get("completion_observed") is not (item["status"] != "UNKNOWN")):
            raise ValueError("independent cleanup class observation inconsistent")
        if kind in RESOURCE_CLASSES:
            count = item.get("possible_remnants")
            if item["status"] == "UNKNOWN":
                if count is not None:
                    raise ValueError("unobserved cleanup class invented a count")
            elif type(count) is not int or count < 0 or (count == 0) != (item["status"] == "PASS"):
                raise ValueError("independent cleanup class count inconsistent")
            else:
                identities, omitted = item.get("observed_identities"), item.get("identities_omitted")
                if (not isinstance(identities, list) or type(omitted) is not int or omitted < 0
                        or len(identities) + omitted != count
                        or any(not isinstance(identity, dict) or not all(isinstance(identity.get(key), str) and identity[key] for key in ("id", "name"))
                               for identity in identities)):
                    raise ValueError("independent cleanup identity evidence incomplete")
    statuses = {item["status"] for item in classes.values()}
    expected = "ERROR" if "ERROR" in statuses else "UNKNOWN" if "UNKNOWN" in statuses else "PASS"
    completed = all(item["completion_observed"] for item in classes.values())
    if value["status"] != expected or value.get("completion_observed") is not completed:
        raise ValueError("independent cleanup aggregate inconsistent")
    return value


def trustworthy_observation(value):
    try:
        validated = validate_observation(value, value.get("exit_code"))
        return validated["completion_observed"] is True and validated["status"] in ("PASS", "ERROR")
    except (AttributeError, TypeError, ValueError):
        return False


def qualify_observations(before, after):
    assert trustworthy_observation(before), "Pre-intervention independent inventory unavailable or incomplete"
    assert trustworthy_observation(after) and after["status"] == "PASS", "Final independent absence not established"


def independent_check(output, phase):
    result, reported = None, None
    try:
        result = guards.capture(["bash", str(ROOT / "scripts/pilot-cleanup-check.sh")], 60)
        (output / ("independent-" + phase + ".stdout.txt")).write_bytes(result.stdout)
        (output / ("independent-" + phase + ".stderr.txt")).write_bytes(result.stderr)
        reported = strict_json(result.stdout)
        return validate_observation(reported, result.returncode)
    except (OSError, ValueError, TypeError, RecursionError, subprocess.SubprocessError, guards.QualificationError):
        # The original streams remain beside this record. A completed process
        # does not establish complete inventories, and exit 2 is not ERROR.
        return {"completion_observed": False, "exit_code": result.returncode if result else None, "status": "UNKNOWN",
                "process_completion_observed": result is not None, "reason_codes": ["independent_cleanup_record_unavailable_or_inconsistent"],
                "reported_record": reported if isinstance(reported, dict) else None}


def retain_native_cleanup(output):
    report = None
    try:
        path = output / "stdout.json"
        if path.stat().st_size <= 8 * 1024 * 1024:
            candidate = strict_json(path.read_text())
            if (isinstance(candidate, dict) and candidate.get("status") in ("pass", "warn", "fail", "blocked", "skipped", "error")
                    and isinstance(candidate.get("evidence"), list)):
                ids = [item.get("experiment_id") for item in candidate["evidence"] if isinstance(item, dict)]
                if (len(ids) == len(candidate["evidence"]) and all(isinstance(key, str) and key for key in ids)
                        and len(ids) == len(set(ids)) and ids.count("environment-cleanup") == 1):
                    report = candidate
    except (OSError, ValueError, TypeError, RecursionError):
        pass
    cleanup = next((item for item in report["evidence"] if isinstance(item, dict) and item.get("experiment_id") == "environment-cleanup"), None) if report else None
    status = cleanup.get("status") if cleanup else None
    write_json(output / "cloudforge-cleanup.json", {"source": "original_native_report", "result": status.upper() if status in ("pass", "error", "skipped") else "UNKNOWN", "evidence": cleanup})
    return report


def main():
    if sys.argv[1:2] == ["__tool"]:
        return tool_wrapper(sys.argv[2], sys.argv[3:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--case", choices=CASES, required=True)
    args = parser.parse_args()
    guards.require_runner()
    args.output.mkdir(parents=True, exist_ok=False)
    docker, k3d = shutil.which("docker"), shutil.which("k3d")
    if not docker or not k3d:
        raise guards.QualificationError("fault_runtime_tool_unavailable")
    with tempfile.TemporaryDirectory(prefix="cloudforge-cleanup-fault-") as temporary:
        root = Path(temporary).resolve()
        shutil.copytree(ROOT / "testdata/healthy-node", root / "app")
        (root / "app/cloudforge.yaml").write_text("schema_version: v1alpha1\nruntime: {port: 8080}\nendpoints: {health: /health, readiness: /ready}\n")
        manifest = root / "app/k8s/app.yaml"
        manifest.write_text(manifest.read_text().split("---")[0])
        state = {"case": args.case, "harness_token": secrets.token_hex(16), "resources": {}, "source_hashes": guards.hashes(root / "app")}
        write_json(root / "private-state.json", state, private=True)
        write_json(args.output / "fault-policy.json", {"case": args.case, "source_hashes": state["source_hashes"], "retries": 0,
                   "fault_scope": "copied_bundled_public_fixture", "expected_native": {"status": "ERROR", "exit_code": 2},
                   "expected_native_cleanup": "PASS" if args.case in PASS_CLEANUP else "ERROR",
                   "proof_boundary": "Installed binary only when supplied binary is the verified canonical archive; local mocked tests are source-level proof."})
        bin_dir = root / "bin"
        bin_dir.mkdir()
        environment = dict(os.environ, CF_CLEANUP_FAULT_ROOT=str(root), CF_CLEANUP_REAL_DOCKER=docker,
                           CF_CLEANUP_REAL_K3D=k3d, PATH=str(bin_dir) + os.pathsep + os.environ["PATH"], TMPDIR=str(root))
        for tool in ("docker", "k3d"):
            wrapper = bin_dir / tool
            wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable," + repr(str(Path(__file__).resolve())) + ", '__tool', " + repr(tool) + ", *sys.argv[1:]])\n")
            wrapper.chmod(0o700)
        session, result, before, teardown, caught = Session(root, docker), None, None, [], None
        try:
            sentinel = "pilot-cleanup-existing-" + secrets.token_hex(6)
            for kind in ("network", "volume"):
                session.create_sentinel("unrelated-" + kind, kind, sentinel)
            result = run_observed([str(args.binary.resolve()), "verify", str(root / "app"), "--format", "json"], args.output / "stdout.json", timeout=900, env=environment)
        except (OSError, subprocess.SubprocessError, guards.QualificationError) as error:
            caught = type(error).__name__
        finally:
            report = retain_native_cleanup(args.output)
            session = Session(root, docker)
            before_inventory = independent_check(args.output, "before-intervention")
            try:
                before = session.observe()
                write_json(args.output / "observer-before-intervention.json", {"available": True, "resources": before, "independent_inventory": before_inventory,
                           "native_workspace_count": len(list(root.glob("cloudforge-verify-*")))})
            except (OSError, ValueError, guards.QualificationError):
                write_json(args.output / "observer-before-intervention.json", {"available": False, "status": "UNKNOWN", "independent_inventory": before_inventory})
            # Removing a captured resource is harness intervention. Never turn
            # its success into a CloudForge cleanup PASS or erase native ERROR.
            deadline = time.monotonic() + 120
            for role, proof in session.state["resources"].items():
                try:
                    if time.monotonic() >= deadline:
                        raise guards.QualificationError("harness_teardown_deadline")
                    outcome = session.remove(proof)
                    teardown.append({"role": role, "outcome": outcome})
                except (OSError, ValueError, guards.QualificationError):
                    teardown.append({"role": role, "outcome": "UNKNOWN_OR_REFUSED"})
            write_json(args.output / "harness-teardown.json", {"scope": "captured_identity_and_private_marker_only", "actions": teardown,
                       "scratch_directory_removal": "Harness temporary directory is removed on exit; any remaining native workspace makes qualification fail.",
                       "native_workspace_count": len(list(root.glob("cloudforge-verify-*")))})
            final_observation = independent_check(args.output, "after-intervention")
            write_json(args.output / "observer-after-intervention.json", final_observation)
            if (root / "events.jsonl").exists():
                shutil.copyfile(root / "events.jsonl", args.output / "fault-events.jsonl")
        assert caught is None and result is not None, "Native execution unavailable; original streams retained"
        assert report is not None, "Native report unusable; original streams retained"
        assert session.state.get("injected"), "Declared fault was never exercised"
        assert before is not None, "Pre-intervention observation unavailable"
        qualify_observations(before_inventory, final_observation)
        events = [json.loads(line) for line in (root / "events.jsonl").read_text().splitlines()]
        qualify(report, result.returncode, args.case, before, events)
        assert all(item["outcome"] != "UNKNOWN_OR_REFUSED" for item in teardown), "Harness teardown failed"
        assert not list(root.glob("cloudforge-verify-*")), "Native private workspace remained"
    print(args.case + ": expected native ERROR retained; native cleanup and harness intervention recorded separately")
    return 0


if __name__ == "__main__":
    sys.exit(main())
