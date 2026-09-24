"""Bind public-fixture local import capture to existing product calls, without extra runtime queries."""
import json
import os
from pathlib import Path
import re
import stat
import tempfile

RUN = re.compile(r"cloudforge-[a-f0-9]{8,32}")
IDENTITY = re.compile(r"[a-f0-9]{64}")
STAGING = re.compile(r"cloudforge-import-[0-9]{1,10}")
CTR = ["ctr", "--address", "/run/k3s/containerd/containerd.sock", "--namespace", "k8s.io", "images", "import", "--local", "--all-platforms"]


def read_state(path):
    if not path.exists():
        return {}
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > 8192):
            raise ValueError("unusable private import registration")
        value = json.loads(stream.read(8193))
    if not isinstance(value, dict):
        raise ValueError("unusable private import registration")
    return value


def save_state(path, value):
    descriptor, temporary = tempfile.mkstemp(prefix=".import-registration-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w") as stream:
            json.dump(value, stream, sort_keys=True)
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def node_template(run, token):
    node = "k3d-" + run + "-server-0"
    return ('{"id":{{json .Id}},"name":{{json (eq .Name "/' + node + '")}},'
            '"owner":{{json (eq (index .Config.Labels "cloudforge.dev/ownership") "' + token + '")}},'
            '"run":{{json (eq (index .Config.Labels "cloudforge.dev/run-id") "' + run + '")}},'
            '"cluster":{{json (eq (index .Config.Labels "k3d.cluster") "' + run + '")}},'
            '"role":{{json (eq (index .Config.Labels "k3d.role") "server")}},"running":{{json .State.Running}}}')


def strict_object(payload):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate node identity field")
            value[key] = item
        return value
    return json.loads(payload, object_pairs_hook=unique)


def is_import(arguments):
    return len(arguments) > 2 and arguments[0] == "exec" and "import" in arguments[3:]


def validate_import(arguments, state):
    node, archive = state.get("node"), state.get("node_archive")
    if (not isinstance(node, str) or not IDENTITY.fullmatch(node) or not isinstance(archive, str)
            or arguments != ["exec", node, *CTR, archive] or not state.get("image")):
        raise ValueError("unregistered public local archive import")


def observe(tool, arguments, state_path, owner, image_names, real, helpers, stdout, stderr):
    """Return (handled, native_exit). State/tee files stay private until final ctr import.

    The caller validates its exact public fixture before this hook. The hook
    never reads or tees image-save stdout, archive contents, or arbitrary exec.
    """
    state_path = Path(state_path)
    state = read_state(state_path)
    if tool == "k3d" and arguments[:2] == ["cluster", "create"]:
        if not owner or not RUN.fullmatch(owner) or arguments[2] != owner:
            raise ValueError("registered generated run required")
        labels = [arguments[index + 1] for index, value in enumerate(arguments[:-1]) if value == "--runtime-label"]
        markers = [value.split("=", 1)[1][:-4] for value in labels if re.fullmatch(r"cloudforge.dev/ownership=[a-f0-9]{32}@all", value)]
        if (len(markers) != 1 or labels.count("cloudforge.dev/owned=true@all") != 1
                or labels.count("cloudforge.dev/run-id=" + owner + "@all") != 1):
            raise ValueError("exact private cluster ownership required")
        if state and state.get("run") != owner:
            raise ValueError("multiple import owners")
        save_state(state_path, {"run": owner, "token": markers[0]})
        return False, None
    if tool != "docker":
        return False, None
    if arguments[:2] == ["container", "inspect"] and len(arguments) == 5 and arguments[-1] == "k3d-" + str(owner) + "-server-0":
        if not state.get("token") or arguments != ["container", "inspect", "--format", node_template(owner, state["token"]), arguments[-1]]:
            # Other product inspection formats are passed through but cannot
            # establish ownership for a later capture.
            return False, None
        state.pop("node", None)
        state.pop("node_archive", None)
        save_state(state_path, state)
        with tempfile.TemporaryDirectory(prefix="private-node-observation-", dir=state_path.parent) as temporary:
            private = Path(temporary)
            code = helpers.tee_command([real, *arguments], private, "ownership", stdout, stderr,
                                       tool="docker", name="node-ownership", native_timeout_seconds=10)
            records = list(private.glob("*.json"))
            try:
                record = json.loads(records[0].read_text()) if len(records) == 1 else {}
                retained = record["streams"]["stdout"]
                payload = (private / retained["file"]).read_bytes()
                value = strict_object(payload)
                if (code == 0 and record.get("completion_observed") is True and retained["truncated"] is False
                        and set(value) == {"id", "name", "owner", "run", "cluster", "role", "running"}
                        and isinstance(value["id"], str) and IDENTITY.fullmatch(value["id"])
                        and all(value[key] is True for key in value if key != "id")):
                    state["node"] = value["id"]
                    save_state(state_path, state)
            except (ValueError, KeyError, OSError, TypeError):
                pass  # Missing auxiliary registration never changes native bytes/exit.
            return True, code
    if arguments[:2] == ["image", "save"]:
        if len(arguments) != 3 or state.get("run") != owner or not state.get("node"):
            raise ValueError("public export requires observed owned node")
        images = ["cloudforge/" + name + ":" + owner.removeprefix("cloudforge-") + "-" + suffix
                  for name in image_names for suffix in ("a", "b")]
        if arguments[2] not in images:
            raise ValueError("unrelated image export")
        state.update(image=arguments[2])
        state.pop("node_archive", None)
        save_state(state_path, state)
        return False, None  # Binary archive streams MUST go directly to the product's bounded file sink.
    if arguments[:1] == ["cp"] and len(arguments) == 3 and (state.get("image") or ":/tmp/cloudforge-import-" in arguments[2]):
        source = Path(arguments[1])
        node, _, destination = arguments[2].partition(":")
        target = Path(destination)
        workspace = Path(os.environ.get("TMPDIR", "")).parent
        if (not state.get("image") or node != state.get("node") or not source.is_absolute()
                or source.resolve() != source or source.name != "image.tar" or not STAGING.fullmatch(source.parent.name)
                or source.parent.parent != workspace or not workspace.name.startswith("cloudforge-verify-")
                or target != Path("/tmp") / source.parent.name / "image.tar"):
            raise ValueError("unrelated archive copy")
        info = source.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600
                or not 0 < info.st_size <= 4 * 1024 * 1024 * 1024):
            raise ValueError("nonregular archive source")
        for directory in (source.parent, workspace):
            information = directory.lstat()
            if (not stat.S_ISDIR(information.st_mode) or information.st_uid != os.getuid()
                    or stat.S_IMODE(information.st_mode) != 0o700):
                raise ValueError("private archive directory required")
        state["node_archive"] = str(target)
        save_state(state_path, state)
        return False, None
    if is_import(arguments):
        if not isinstance(owner, str) or not RUN.fullmatch(owner) or state.get("run") != owner:
            raise ValueError("registered generated run required")
        images = ["cloudforge/" + name + ":" + owner.removeprefix("cloudforge-") + "-" + suffix
                  for name in image_names for suffix in ("a", "b")]
        if state.get("image") not in images:
            raise ValueError("unrelated registered image")
        validate_import(arguments, state)
        return False, None  # Caller tees this exact text-only import command.
    return False, None
