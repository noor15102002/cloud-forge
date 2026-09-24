#!/usr/bin/env python3
"""Validate independent public Redis cases, retaining every native outcome unchanged."""
import argparse
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
from qualification_command import run_observed
from qualification_record import valid_cleanup
from core_fixture import CONTROL, ROOT, prepare, require_core_healthy, require_core_scope
from reliability_import_observer import helpers, public_files
import local_import_observer as local_import

CASES = ("healthy", "semantic-degraded", "disconnected", "dependency-timeout")



def import_wrapper(tool, arguments):
    helpers.require_runner()
    fixture = os.environ["CF_DEPENDENCY_FIXTURE"]
    case = os.environ["CF_DEPENDENCY_CASE"]
    application = Path(os.environ["CF_DEPENDENCY_APPLICATION"])
    core = os.environ["CF_DEPENDENCY_CORE"] == "true"
    names = {"healthy-node-redis": "healthy-node-redis", "healthy-python-redis": "healthy-python-redis-api"}
    if fixture not in names or case not in CASES or tool not in ("docker", "k3d"):
        raise ValueError("unknown public Redis observation")
    if (application.resolve() != application or application.is_symlink()
            or core and (application.name != "application" or not application.parent.name.startswith("cf-core-redis-"))
            or not core and application != ROOT / "testdata" / fixture):
        raise ValueError("public Redis fixture required")
    expected = public_files(ROOT / "testdata" / fixture)
    if core:
        expected["cloudforge.yaml"] = expected["cloudforge.yaml"].replace(CONTROL.encode(), b"")
        for name, payload in expected.items():
            if name.endswith(".yaml") and name != "cloudforge.yaml":
                documents = re.split(r"(?m)^---\s*\n", payload.decode())
                selected = [doc for doc in documents if not re.search(r"(?m)^kind: HorizontalPodAutoscaler\s*$", doc)]
                if len(selected) != len(documents):
                    expected[name] = "---\n".join(selected).encode()
    if public_files(application) != expected:
        raise ValueError("public Redis fixture changed")
    state_path = Path(os.environ["CF_DEPENDENCY_IMPORT_STATE"])
    owner = local_import.read_state(state_path).get("run")
    if tool == "k3d" and arguments[:2] == ["cluster", "create"]:
        owner = arguments[2]
    real = os.environ["CF_REAL_IMPORT_" + tool.upper()]
    handled, code = local_import.observe(tool, arguments, state_path, owner, [names[fixture]], real,
                                        helpers, sys.stdout.buffer, sys.stderr.buffer)
    if not handled and tool == "docker" and local_import.is_import(arguments):
        code = helpers.tee_command([real, *arguments], Path(os.environ["CF_DEPENDENCY_IMPORT_OUTPUT"]), case,
                                   sys.stdout.buffer, sys.stderr.buffer, tool="docker", name="local-image-import",
                                   scope="bundled_public_redis_fixture_only", native_timeout_seconds=180)
        handled = True
    if not handled:
        os.execv(real, [real, *arguments])
    if code < 0:
        import signal
        received = -code
        if received not in (signal.SIGKILL, signal.SIGSTOP):
            signal.signal(received, signal.SIG_DFL)
        os.kill(os.getpid(), received)
    return code


class StopCases(RuntimeError):
    """Further runtime work is unsafe or its cancellation state is uncertain."""


def read_object(path):
    value = json.loads(Path(path).read_text())
    if not isinstance(value, dict):
        raise ValueError("expected a JSON object")
    return value


def write_record(path, value):
    with Path(path).open("x") as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write("\n")


def run_case(args, application, name, row, config=None, env=None):
    path = args.output / f"{name}.json"
    baseline = args.output / f"{name}.cleanup-baseline.json"
    cleanup_path = args.output / f"{name}.cleanup.json"
    cleanup_command = ["bash", "scripts/pilot-cleanup-check.sh"]
    try:
        subprocess.run([*cleanup_command, "--capture-baseline", str(baseline)],
                       capture_output=True, text=True, check=True, timeout=30)
        if read_object(baseline).get("status") != "PASS":
            raise ValueError("cleanup baseline unavailable")
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        raise StopCases(f"cleanup baseline could not be established: {type(error).__name__}") from error

    command = [args.binary, "verify", str(application), "--format", "json"]
    if config:
        command += ["--config", config]
    result, execution_error = None, None
    with tempfile.TemporaryDirectory(prefix="cf-dependency-private-") as private:
        environment = dict(os.environ if env is None else env, TMPDIR=private)
        # Tee only the registered public image's local ctr import. Image-save
        # stdout is an archive and always passes directly to the product sink.
        for tool in ("k3d", "docker"):
            wrapper = Path(private) / tool
            wrapper.write_text(textwrap.dedent('''\
    #!/usr/bin/env python3
    import os, pathlib, sys
    os.execv(sys.executable, [sys.executable, os.environ['CF_DEPENDENCY_HARNESS'], '__import', pathlib.Path(sys.argv[0]).name, *sys.argv[1:]])
    '''))
            wrapper.chmod(0o700)
        environment.update(CF_REAL_IMPORT_K3D=shutil.which("k3d"), CF_REAL_IMPORT_DOCKER=shutil.which("docker"),
                           CF_IMPORT_LOG=str((args.output / f"{name}.image-import.txt").resolve()),
                           CF_DEPENDENCY_HARNESS=str(Path(__file__).resolve()), CF_DEPENDENCY_FIXTURE=args.fixture,
                           CF_DEPENDENCY_CASE=name, CF_DEPENDENCY_APPLICATION=str(application.resolve()),
                           CF_DEPENDENCY_CORE=str(args.core).lower(), CF_DEPENDENCY_IMPORT_STATE=str(Path(private) / "local-import.json"),
                           CF_DEPENDENCY_IMPORT_OUTPUT=str((args.output / (name + ".image-import")).resolve()))
        environment["PATH"] = private + os.pathsep + environment["PATH"]
        try:
            # The enclosing qualification runner owns this complete process
            # session, including wrappers and children, for cancellation.
            row["native_invocation_attempted"] = True
            result = run_observed(command, path, timeout=1000, env=environment)
        except (OSError, ValueError, subprocess.SubprocessError, KeyboardInterrupt) as error:
            execution_error = error
        finally:
            # Observe before the harness removes its private directory. A leak
            # remains a failure even if TemporaryDirectory then removes it.
            row["private_workspace_cleanup"] = not list(Path(private).glob("cloudforge-verify-*"))
            try:
                subprocess.run([*cleanup_command, "--baseline", str(baseline), "--output", str(cleanup_path)],
                               capture_output=True, text=True, check=True, timeout=60)
                row["independent_cleanup"] = valid_cleanup(read_object(cleanup_path))
            except (OSError, ValueError, subprocess.SubprocessError):
                row["independent_cleanup"] = False

    # Read retained native files without altering or replacing any of them.
    try:
        report = read_object(path)
        exit_record = read_object(path.with_suffix(".exit.json"))
        row["native_status"] = report.get("status")
        row["native_exit_code"] = exit_record.get("exit_code")
    except (OSError, ValueError) as error:
        raise StopCases(f"native completion/cancellation state is unavailable: {type(error).__name__}") from error
    dependencies = report.get("dependencies")
    redis = dependencies[0].get("status", "not_observed") if isinstance(dependencies, list) and dependencies and isinstance(dependencies[0], dict) else "not_observed"
    print(f"{name}: exit={row['native_exit_code']}; status={row['native_status']}; redis={redis}", flush=True)
    if execution_error is not None:
        raise StopCases(f"native execution interrupted or unavailable: {type(execution_error).__name__}") from execution_error
    if (exit_record.get("completion_observed") is not True or exit_record.get("outer_timeout") is not False
            or result is None or type(row["native_exit_code"]) is not int
            or row["native_exit_code"] != result.returncode):
        raise StopCases("native completion was not reliably observed")
    diagnostics = report.get("diagnostics", [])
    if not isinstance(diagnostics, list) or any(not isinstance(item, dict) for item in diagnostics):
        raise StopCases("native cancellation diagnostics are unusable")
    if result.returncode < 0 or result.returncode in (130, 143) or any(
            "cancel" in str(item.get("code", "")) for item in diagnostics):
        raise StopCases("native cancellation or signal termination observed")
    if not row["private_workspace_cleanup"] or not row["independent_cleanup"]:
        raise StopCases("independent cleanup or private workspace absence could not be confirmed")
    evidence_rows = report.get("evidence", [])
    if not isinstance(evidence_rows, list) or any(not isinstance(item, dict) for item in evidence_rows):
        raise StopCases("native cleanup evidence is unusable")
    cleanup = [item for item in evidence_rows if item.get("experiment_id") == "environment-cleanup"]
    if len(cleanup) != 1 or cleanup[0].get("status") != "pass":
        raise StopCases("native cleanup did not report an unambiguous PASS")
    assert report["schema_version"] == "v1alpha8", "unexpected native report schema"
    # Exercise the strict user/Action report loader, after cleanup is confirmed.
    rendered = subprocess.run([args.binary, "report", str(path), "--format", "json"], capture_output=True, text=True, check=True, timeout=30)
    repeat = subprocess.run([args.binary, "report", str(path), "--format", "json"], capture_output=True, text=True, check=True, timeout=30)
    assert rendered.stdout == repeat.stdout, "report rendering is not deterministic"
    evidence = {item["experiment_id"]: item for item in evidence_rows}
    if args.core:
        require_core_scope(report)
    return result, report, evidence


def validate_case(name, result, report, evidence, core):
    if name == "healthy":
        assert result.returncode == 0, report.get("diagnostics")
        required = ["dependency.redis", "container-build", "deployment-readiness", "semantic-readiness", "pod-recovery", "rolling-deployment", "load-profile"]
        if core:
            require_core_healthy(report, result.returncode, redis=True)
        else:
            required += ["readiness-gating", "inflight-shutdown"]
        for experiment in required:
            assert evidence[experiment]["status"] == "pass", evidence[experiment]
        assert report["dependencies"][0]["network_exposure"] == "cluster-internal"
        assert "@sha256:" in report["dependencies"][0]["image"]
        assert report["fingerprint"]["compatibility_key"]
        assert report["fingerprint"]["dependencies"][0]["digest"].startswith("sha256:")
        assert evidence["dependency-loss"]["status"] == "skipped"
    elif name in ("semantic-degraded", "disconnected"):
        assert result.returncode == 1 and report["status"] == "fail", "expected native FAIL/1, not a CloudForge execution error"
        assert evidence["dependency.redis"]["status"] == "pass"
        assert evidence["deployment-readiness"]["status"] == "fail"
        assert evidence["semantic-readiness"]["status"] == "fail"
        if name == "semantic-degraded":
            measurements = {item["name"]: item["value"] for item in evidence["semantic-readiness"]["measurements"]}
            assert measurements["http_status"] == "200"
            assert measurements["json_status_matched"] == "false"
    else:
        assert name == "dependency-timeout"
        assert result.returncode == 1 and report["status"] == "blocked", "expected native BLOCKED/1"
        assert evidence["dependency.redis"]["status"] == "fail"
        assert evidence["deployment-readiness"]["status"] == "blocked"
        assert not any(item["id"] == "container.startup" for item in report["findings"])


def run_timeout_case(args, application, row):
    # Deterministic infrastructure fault injection belongs to this disposable
    # harness, not product config: delay Redis readiness past its 1s budget.
    with tempfile.TemporaryDirectory(prefix="cf-redis-fault-") as temporary:
        wrapper = Path(temporary) / "kubectl"
        real = shutil.which("kubectl")
        wrapper.write_text(textwrap.dedent('''\
    #!/usr/bin/env python3
    import os, pathlib, re, sys
    args = sys.argv[1:]
    if 'apply' in args and args[-1].endswith('dependency-redis.yaml'):
        path = pathlib.Path(args[-1])
        value = path.read_text()
        value = re.sub(r'(?m)^(\\s+)periodSeconds: 1$', r'\\1initialDelaySeconds: 60\\n\\1periodSeconds: 1', value)
        path.write_text(value)
    os.execv(os.environ['CF_REAL_KUBECTL'], [os.environ['CF_REAL_KUBECTL'], *args])
    '''))
        wrapper.chmod(0o700)
        environment = dict(os.environ, PATH=temporary + os.pathsep + os.environ["PATH"], CF_REAL_KUBECTL=real)
        return run_case(args, application, "dependency-timeout", row, "testdata/redis-failures/startup-timeout.yaml", environment)


def run_suite(args, profile_root):
    args.binary = str(Path(args.binary).resolve())
    args.output.mkdir(parents=True, exist_ok=True)
    # Never resume or overwrite a partial attempt. Every invocation gets its
    # own output directory; earlier failures remain authoritative evidence.
    retained = [args.output / "dependency-suite.json"]
    retained += [args.output / f"{name}{suffix}" for name in CASES for suffix in
                 (".json", ".stderr.txt", ".exit.json", ".cleanup.json", ".cleanup-baseline.json", ".image-import.txt", ".image-import")]
    if any(path.exists() or path.is_symlink() for path in retained):
        raise ValueError("dependency evidence already exists; use a new output directory")
    application = Path("testdata") / args.fixture
    if args.core:
        application = prepare(args.fixture, Path(profile_root) / "application", args.output / "core-profile.json")
    rows, stop_reason = [], None
    for name in CASES:
        row = {"case": name, "contract_status": "FAIL", "native_status": None, "native_exit_code": None,
               "native_invocation_attempted": False,
               "independent_cleanup": False, "private_workspace_cleanup": False, "failures": []}
        rows.append(row)
        try:
            if name == "dependency-timeout":
                result, report, evidence = run_timeout_case(args, application, row)
            else:
                config = None if name == "healthy" else f"testdata/redis-failures/{name}.yaml"
                result, report, evidence = run_case(args, application, name, row, config)
            validate_case(name, result, report, evidence, args.core)
            row["contract_status"] = "PASS"
        except (StopCases, KeyboardInterrupt, subprocess.TimeoutExpired) as error:
            stop_reason = str(error) or type(error).__name__
            row["failures"].append(stop_reason)
            break
        except (AssertionError, KeyError, IndexError, TypeError, ValueError, OSError, subprocess.CalledProcessError) as error:
            row["failures"].append(f"{type(error).__name__}: {error}")
            # Only contract assertions/strict report loading may fail after the
            # run has passed both cleanup observations. Never retry this case.
            if not row["independent_cleanup"] or not row["private_workspace_cleanup"]:
                stop_reason = "case failed before independent cleanup was confirmed"
                break
        finally:
            print(f"{name}: qualification={row['contract_status']}", flush=True)
    passed = len(rows) == len(CASES) and all(row["contract_status"] == "PASS" for row in rows)
    not_started = [row["case"] for row in rows if not row["native_invocation_attempted"]] + list(CASES[len(rows):])
    summary = {"schema_version": "qualification-dependencies-v1", "status": "PASS" if passed else "FAIL",
               "all_cases_attempted": not not_started, "stopped_reason": stop_reason,
               "not_started": not_started, "cases": rows}
    write_record(args.output / "dependency-suite.json", summary)
    for row in rows:
        for failure in row["failures"]:
            print(f"{row['case']}: {failure}", file=sys.stderr, flush=True)
    return 0 if passed else 1


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("fixture", choices=["healthy-node-redis", "healthy-python-redis"])
    parser.add_argument("output", type=Path)
    parser.add_argument("--core", action="store_true", help="Use an explicit HTTP-core profile without experimental HPA/control tests")
    args = parser.parse_args(argv)
    with tempfile.TemporaryDirectory(prefix="cf-core-redis-") as profile_root:
        return run_suite(args, profile_root)


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "__import":
        try:
            sys.exit(import_wrapper(sys.argv[2], sys.argv[3:]))
        except (ValueError, KeyError, OSError, helpers.QualificationError):
            print("Public Redis import observation could not be established.", file=sys.stderr)
            sys.exit(2)
    sys.exit(main())
