"""Versioned release metadata; wall-clock observations are not binary identity."""
from datetime import datetime, timezone

LEGACY_FIELDS = {"version", "commit", "date", "source_date_epoch", "go_version", "os", "arch", "archive", "archive_sha256", "binary_sha256"}
WALL_CLOCK_FIELDS = {"qualification_build_started_at", "qualification_build_finished_at"}
ENRICHED_FIELDS = LEGACY_FIELDS | WALL_CLOCK_FIELDS | {"manifest_schema_version", "config_schemas", "report_schemas", "expected_runtime_tools"}
CONFIG_SCHEMAS = {"current": "v1alpha7", "supported": [f"v1alpha{i}" for i in range(1, 8)]}
REPORT_SCHEMAS = {"current": "v1alpha8", "supported": [f"v1alpha{i}" for i in range(1, 9)]}
EXPECTED_RUNTIME_TOOLS = {
    "pinned": {"k3d": "v5.9.0", "kubectl": "v1.35.5", "trivy": "v0.74.0", "k6": "v2.2.0", "k3s_node_image": "rancher/k3s:v1.35.5-k3s1"},
    "runner_provided": ["docker", "buildx"],
}


def utc_timestamp(value):
    if not isinstance(value, str):
        raise ValueError("release timestamp must be a UTC string")
    try:
        parsed = datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except ValueError as error:
        raise ValueError("invalid release UTC timestamp") from error
    if parsed.strftime("%Y-%m-%dT%H:%M:%SZ") != value:
        raise ValueError("noncanonical release UTC timestamp")
    return parsed


def validate_manifest_shape(manifest, *, require_enriched=False):
    if not isinstance(manifest, dict):
        raise ValueError("unsupported release manifest")
    if set(manifest) == LEGACY_FIELDS and not require_enriched:
        return
    if set(manifest) != ENRICHED_FIELDS or type(manifest.get("manifest_schema_version")) is not int or manifest["manifest_schema_version"] != 2:
        raise ValueError("unsupported release manifest")
    if manifest["config_schemas"] != CONFIG_SCHEMAS or manifest["report_schemas"] != REPORT_SCHEMAS or manifest["expected_runtime_tools"] != EXPECTED_RUNTIME_TOOLS:
        raise ValueError("unsupported release schema or runtime contract")
    start = utc_timestamp(manifest["qualification_build_started_at"])
    finish = utc_timestamp(manifest["qualification_build_finished_at"])
    if finish < start:
        raise ValueError("release build timestamps are reversed")


def enrich_manifest(manifest, started_at, finished_at):
    return {**manifest, "manifest_schema_version": 2, "config_schemas": CONFIG_SCHEMAS,
            "report_schemas": REPORT_SCHEMAS, "expected_runtime_tools": EXPECTED_RUNTIME_TOOLS,
            "qualification_build_started_at": started_at, "qualification_build_finished_at": finished_at}


def deterministic_manifest(manifest):
    validate_manifest_shape(manifest, require_enriched=True)
    return {key: value for key, value in manifest.items() if key not in WALL_CLOCK_FIELDS}
