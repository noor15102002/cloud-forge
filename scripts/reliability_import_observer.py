"""Bounded import evidence for the bundled public topology fixtures only.

The product retains its original importer, deadlines, exits and cleanup. This
observer adds no retry, runtime query, or application/container log collection.
"""
from contextlib import contextmanager
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import sys

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("bounded_import_evidence", ROOT / "scripts/pilot-backend-cancellation.py")
helpers = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(helpers)
PREFIX = "CF_RELIABILITY_IMPORT_"
CASES = ("source-1", "source-2", "generated-1")
RUN_NAME = re.compile(r"cloudforge-[a-f0-9]{20}")
CONFIG = b"schema_version: v1alpha1\nruntime: {port: 8080}\nendpoints: {health: /health, readiness: /ready}\n"


def public_files(root):
    """Reject links/special files before reading, with fixed small bounds."""
    result, pending, entries, total = {}, [root], 0, 0
    while pending:
        directory = pending.pop()
        if not stat.S_ISDIR(directory.lstat().st_mode):
            raise helpers.QualificationError("import_observer_nonregular_source")
        for path in sorted(directory.iterdir()):
            entries += 1
            if entries > 64:
                raise helpers.QualificationError("import_observer_source_limit")
            information = path.lstat()
            if stat.S_ISDIR(information.st_mode):
                pending.append(path)
            elif stat.S_ISREG(information.st_mode):
                if information.st_size > 1024 * 1024:
                    raise helpers.QualificationError("import_observer_source_limit")
                # O_NOFOLLOW also refuses a link substituted after lstat.
                descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                with os.fdopen(descriptor, "rb") as stream:
                    if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                        raise helpers.QualificationError("import_observer_nonregular_source")
                    data = stream.read(1024 * 1024 + 1)
                total += len(data)
                if len(data) > 1024 * 1024 or total > 2 * 1024 * 1024:
                    raise helpers.QualificationError("import_observer_source_limit")
                result[path.relative_to(root).as_posix()] = data
            else:
                raise helpers.QualificationError("import_observer_nonregular_source")
    return result


def validate_fixture(fixture, case):
    helpers.require_runner()
    if (case not in CASES or fixture.is_symlink() or fixture.name != "application"
            or not fixture.parent.name.startswith("cf-reliability-") or fixture.resolve() != fixture):
        raise helpers.QualificationError("import_observer_requires_public_topology_fixture")
    expected = public_files(ROOT / "testdata/healthy-node")
    if case == "generated-1":
        expected = {name: data for name, data in expected.items() if not name.startswith("k8s/")}
    else:
        manifest = expected["k8s/app.yaml"].decode().split("---")[0]
        expected["k8s/app.yaml"] = manifest.replace("replicas: 2", "replicas: " + case[-1]).replace(
            "readinessProbe:\n", "readinessProbe:\n            initialDelaySeconds: 8\n").encode()
    expected["cloudforge.yaml"] = CONFIG
    if public_files(fixture) != expected:
        raise helpers.QualificationError("import_observer_public_fixture_changed")


def created_run(arguments):
    if len(arguments) < 3 or arguments[:2] != ["cluster", "create"] or not RUN_NAME.fullmatch(arguments[2]):
        raise helpers.QualificationError("import_observer_requires_owned_cluster_create")
    owner = arguments[2]
    labels = [arguments[index + 1] for index, value in enumerate(arguments[:-1]) if value == "--runtime-label"]
    if ("cloudforge.dev/owned=true@all" not in labels or "cloudforge.dev/run-id=" + owner + "@all" not in labels
            or not any(re.fullmatch(r"cloudforge.dev/ownership=[a-f0-9]{32}@all", label) for label in labels)):
        raise helpers.QualificationError("import_observer_requires_owned_cluster_create")
    return owner


def validate_import(arguments, owner):
    if (not isinstance(owner, str) or not RUN_NAME.fullmatch(owner) or len(arguments) != 7
            or arguments[:2] != ["image", "import"] or arguments[3:5] != ["--cluster", owner]
            or arguments[5:] != ["--mode", "tools-node"]):
        raise helpers.QualificationError("import_observer_requires_registered_tools_node_import")
    image = "cloudforge/healthy-node-api:" + owner.removeprefix("cloudforge-")
    if arguments[2] not in (image + "-a", image + "-b"):
        raise helpers.QualificationError("import_observer_refused_unrelated_image")


def tool_wrapper(arguments):
    helpers.require_runner()
    real = os.environ[PREFIX + "REAL_K3D"]
    if arguments[:2] not in (["cluster", "create"], ["image", "import"]):
        # In particular, no kubeconfig, cluster logs, or delete output is saved.
        os.execv(real, [real, *arguments])
    fixture, case = Path(os.environ[PREFIX + "FIXTURE"]), os.environ[PREFIX + "CASE"]
    validate_fixture(fixture, case)
    owner_path = fixture.parent / "import-observer" / "owner.json"
    owner = json.loads(owner_path.read_text())["run_name"] if owner_path.exists() else None
    if arguments[:2] == ["cluster", "create"]:
        created = created_run(arguments)
        if owner not in (None, created):
            raise helpers.QualificationError("import_observer_multiple_run_names")
        helpers.write_json(owner_path, {"run_name": created})
        os.execv(real, [real, *arguments])
    validate_import(arguments, owner)
    code = helpers.tee_command([real, *arguments], Path(os.environ[PREFIX + "OUTPUT"]), case,
                               sys.stdout.buffer, sys.stderr.buffer,
                               scope="bundled_public_topology_fixture_only", native_timeout_seconds=180)
    if code < 0:
        received = -code
        if received not in (signal.SIGKILL, signal.SIGSTOP):
            signal.signal(received, signal.SIG_DFL)
        os.kill(os.getpid(), received)
    return code


@contextmanager
def observe_imports(fixture, case, output):
    fixture = fixture.absolute()
    validate_fixture(fixture, case)
    real = shutil.which("k3d")
    if real is None:
        # Preserve the product's own missing-tool diagnosis.
        yield {}
        return
    private = fixture.parent / "import-observer"
    private.mkdir(mode=0o700)
    wrapper = private / "k3d"
    wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable,[sys.executable," + repr(str(Path(__file__).resolve())) + ",*sys.argv[1:]])\n")
    wrapper.chmod(0o700)
    yield {"PATH": str(private) + os.pathsep + os.environ.get("PATH", ""),
           PREFIX + "REAL_K3D": str(Path(real).resolve()), PREFIX + "FIXTURE": str(fixture),
           PREFIX + "CASE": case, PREFIX + "OUTPUT": str(output.resolve())}


if __name__ == "__main__":
    try:
        sys.exit(tool_wrapper(sys.argv[1:]))
    except (helpers.QualificationError, OSError, ValueError, KeyError):
        print("Public topology import observation could not be established.", file=sys.stderr)
        sys.exit(2)
