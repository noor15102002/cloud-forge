# Dependency-aware verification

CloudForge supports one containerized HTTP application and an explicitly enabled,
internal Redis dependency. It does not infer or connect to an existing service.
A detected dependency without an explicit test declaration blocks the runtime
plan; declare `enabled: false` only when it is unnecessary in that test mode.
An enabled unsupported dependency is BLOCKED. The original Redis slice does not include MySQL, RabbitMQ,
Compose execution or general dependency graph is implemented.

```yaml
schema_version: v1alpha2
dependencies:
  redis:
    enabled: true
    startup_timeout: 2m
environment:
  REDIS_URL:
    from: dependency.redis.url
  APP_ENV:
    value: test
endpoints:
  health: /health
  readiness: /ready
  load: /work
readiness:
  status: 200
  json:
    status: ready
load:
  vus: 5
  duration: 20s
```

Omit a port override when the Dockerfile/Deployment identifies one unambiguously.
Omit `endpoints.load` when there is no representative safe GET. Provider-backed
POST work is not replaced with a health-route benchmark. No scripts, custom HTTP
headers, body templates or authentication are added by this milestone.

`cloudforge verify PATH --plan --format json` performs read-only analysis and
returns a deterministic versioned capability plan. It runs no subprocesses,
builds or clusters. Ordinary verification also prints its plan to stderr before
expensive work, preserving one machine-readable report on stdout. Supported
capabilities describe intent; they are not observed PASS results.

## Redis lifecycle and limits

The first-party provider uses `redis:7.4.6-alpine` pinned to the immutable index
`sha256:3b73847e72874be07e6657b129a94761662b79bc0f679273757d4218573b2a98`.
Image/version/digest, resources, configuration mode and startup outcome are
recorded separately from application findings. This digest is the selected
multi-platform index, not a claim that a platform image ID was separately read.
The dependency image is not separately scanned by Trivy in this release; the
existing application image scan remains in place.

Redis has one replica, requests 50m CPU / 64Mi, limits 250m / 256Mi, and a 128MiB
maxmemory cap with noeviction. Persistence is disabled. It runs as non-root with
a read-only root filesystem, no additional capabilities, no service-account
token and no mounted host volumes. Authentication is disabled/test-only in its
unique disposable cluster; no credential generator is needed for this scope.
The ClusterIP has no NodePort, hostPort or public endpoint.

The application, HPA maximum and rollout surge plus Redis must fit the existing
4 CPU / 2Gi workload budget. Redis limits are not extra capacity outside that
budget. Excess is rejected before building or creating the cluster. The private
builder remains bounded to 2 CPU / 2Gi and the cluster to 4Gi.

After cluster bootstrap, CloudForge applies its Redis resources and waits for
Redis CLI PING readiness before applying the application. The startup wait is
bounded (1s–2m, default 2m); manifest application has its own bounded command
window. Dependency startup failure blocks application verification. Cancellation
or an inability to execute/observe the operation is ERROR. Application readiness
failures occur only after dependency readiness has been established.

## Environment bindings

Only `dependency.redis.url` is currently a supported generated binding. Names
must match `[A-Z][A-Z0-9_]{0,63}`; there are at most 32 entries. Explicit literal
values contain at most 64 letters, digits, spaces, dots, underscores or hyphens.
Credential-like names are rejected. Connection-like names must use a generated
binding or an empty disabled value. There is exactly one `from` or `value` per
entry. Values such as booleans should be quoted in YAML.

CloudForge never loads `.env`, production Secrets, a complete shell environment
or cloud credentials into application containers. Dockerfile-defined environment
still belongs to the application image and needs source review. Only generated
manifests receive test bindings; original source manifests are not edited.
Even declared harmless literal values are omitted from the public fingerprint
configuration. A hash distinguishes their effective configurations. This is not
a secret-management API: never put credentials in literals or assertion values.

## Readiness acceptance

The existing `endpoints.readiness` field selects the path. Optional `readiness`
adds an exact successful status (200–299) and up to 16 flat string property
assertions. Nested paths, expressions, coercion, regular expressions and query
languages are unsupported. HTTP 200 with `{"status":"degraded"}` fails an
explicit `status: ready` JSON assertion. A missing/wrong-type key, duplicate JSON
key, malformed response or a body exceeding 64KiB also fails acceptance.

Evidence records transport availability, observed HTTP status, status matching
and each assertion's match result. Response bodies and observed string values
are omitted. Direct HTTP requests have bounded timeouts, no environment proxy
and no redirect following. These assertions also apply to traffic observations
of the readiness endpoint during lifecycle tests.

Semantic assertions are verifier observations. They do **not** rewrite a
Kubernetes HTTP probe into a JSON-aware probe. Kubernetes can still mark a pod
ready when its handler returns 200/degraded; CloudForge's semantic result then
fails independently. Service gating requires the separate explicit control
protocol. Dependency-loss disruption is explicitly SKIPPED in this release.

## Reports and migration

Current verification emits `v1alpha6`; older verification reports remain supported. Selected-workload analysis uses `v1alpha2`; default analysis and doctor retain `v1alpha1`.
Existing `v1alpha1` runtime config remains accepted without the new fields.
Dependency/environment/readiness extensions require `v1alpha2` or `v1alpha3` config. All four
verification report versions remain readable through `cloudforge report` and
the trusted GitHub reporter. Old schemas are retained unchanged.

BLOCKED means a required capability prevents meaningful verification (exit 1).
SKIPPED means an experiment/assertion was inapplicable or intentionally excluded; execution flags separately record any observations performed. FAIL is an observed
application/policy failure, or explicitly labeled dependency failure. ERROR
indicates a CloudForge/environment execution problem or cancellation (exit 2).
Dependency startup FAIL is reported in the dependency section with the run
BLOCKED, not as an application startup finding. Security findings remain a
separate findings collection. A plan-only inspection returns exit 0 when feasible
and 1 when blocked, without running any experiment.

Fingerprints include pinned dependency image/digest/version/resources/mode,
effective bindings and semantic contract. Different schema versions or material
dependency configurations are not silently compared. Incomplete or blocked
reports cannot establish compatible runtime baselines. Timing tolerances remain
observational; the existing 10% heuristic has not become statistically calibrated.

All dependency resources carry run ownership labels inside the unique cluster.
The existing independent cleanup deadlines remove the cluster and its dependency
Deployment, Service and namespace after failure, timeout or cancellation, along
with owned images/builders and private configuration. `--keep-environment`
explicitly retains a created cluster for inspection; SIGKILL/host/daemon failure
can still require manual owned-resource recovery. No global prune is used.

## Backend extension

The current v1alpha5 input contract also supports fixed PostgreSQL/pgvector and
ClamAV providers, generated values, preparation and explicit capacity/network
controls. See [backend verification](backend-runtime.md); the Redis configuration
examples and default profile above remain valid.
