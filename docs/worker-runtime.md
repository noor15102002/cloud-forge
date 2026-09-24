# Bounded worker heartbeat verification

This implemented contract is **EXPERIMENTAL** for v0.1.0-alpha.1. See the
[authoritative support and maturity table](supported-applications.md). Planner
SUPPORTED does not assign stable maturity or certify worker business behavior.

Runtime configuration `v1alpha6` can explicitly select one background process.
Plans and verification reports use `v1alpha8`; every earlier report schema remains
loadable without reinterpretation. The HTTP default and older runtime inputs are
preserved. Worker qualification is tracked in [issue #50](https://github.com/noor15102002/cloud-forge/issues/50).

```yaml
schema_version: v1alpha6
runtime:
  kind: worker
worker:
  command: [node, worker.js]
  heartbeat:
    key: example:worker:heartbeat
    timestamp_field: at
    max_age: 30s
topology:
  replicas: 1
  rollout:
    strategy: recreate
network:
  outbound: declared_dependencies_only
dependencies:
  redis:
    enabled: true
environment:
  REDIS_URL:
    from: dependency.redis.url
```

Build selection, generated test environment, supported providers and bounded
preparation retain the existing backend contract. The direct worker command
contains 1–16 bounded arguments; shell wrappers and inline scripts are rejected.
The Redis key is 1–128 plain letters, digits, colons, underscores, dots, slashes or
hyphens. `max_age` uses `1s` through `60s` or `1m`. No external Redis endpoint is inferred.
Only explicit single-replica `recreate` topology is supported. Worker configuration
rejects HTTP endpoints, ports, readiness assertions, load, control instrumentation,
Services, HPAs and source Kubernetes probe semantics before a build begins. An
image's inherited `EXPOSE` or Docker healthcheck does not create an HTTP runtime.

The worker writes a JSON object with a flat RFC3339 timestamp, for example
`{"at":"2030-01-01T00:00:00.000Z"}`, and an expiring Redis key. CloudForge reads
Redis TIME, expiry and value atomically using a fixed read-only script. Values
larger than 4 KiB, malformed JSON, duplicate fields and invalid/future clocks are
ERROR. Host clock synchronization is not required. A producer timestamp more than
two seconds ahead of Redis, or a backwards clock during observation, makes the
observation unreliable. Valid stale, missing, frozen or nonexpiring heartbeats
that persist through the deadline produce FAIL. Valid heartbeat expiry must be
positive and at most 60 seconds.

Startup requires key absence before application deployment, exactly one owned
Running pod at the expected image/revision, healthy pinned dependencies, and two
advancing fresh heartbeats from the same uninterrupted container instance.
Container restarts or instance changes during this qualification fail the tested
continuity requirement; they are recorded rather than presented as periodic
heartbeat progress. This proves **process liveness only**. It does not establish
queue processor readiness, successful jobs, acknowledgement correctness, request
draining, exactly-once execution, throughput or business-data correctness.

`worker-recovery` and `worker-image-replacement` first scale to zero, wait until all
predecessor pod objects are gone, and wait for the previous key to expire naturally.
CloudForge never deletes that key to manufacture freshness. Only then may one
replacement start. Image B is rebuilt from the same selected source before
runtime resources start; its sequential replacement is not a rolling-availability
claim. Every mutation restores the intended image A and repeats the same worker
and dependency validation, including the original fresh ClamAV signature tuple
when that provider is present. A failure remains FAIL after successful restoration;
independent later evidence continues. Failed restoration blocks dependent work.

Startup, transition and restoration retain the existing two-minute bounds. A
separate final read of at most five seconds can distinguish an observed deadline
failure from an observation error. It cannot extend mutation or turn late progress
into a passing result. Cancellation stops scheduling and triggers the existing
bounded independent cluster, image and builder cleanup.

The plan and report explicitly mark HTTP deployment/semantic readiness, Service
gating, in-flight shutdown, availability, HTTP pod recovery, rolling deployment,
load and HPA experiments SKIPPED. There is no synthetic health endpoint or Service.
Runtime preflight does not require k6 when no load experiment is planned. Reports
retain Kubernetes pod UIDs, image identity, counts, container start time, normalized
heartbeat timing/expiry and restoration evidence. Redis keys, raw values and worker
commands are omitted; a contract hash binds them into baseline compatibility.

The application may start its own housekeeping. CloudForge submits no synthetic
business jobs and does not claim that an empty test database disables all original
application activity. Supported dependency and outbound-policy limitations remain
as documented in [the backend contract](backend-runtime.md).
