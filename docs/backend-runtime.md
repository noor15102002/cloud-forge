# Isolated backend verification

Runtime configuration `v1alpha5` adds fixed PostgreSQL/pgvector and ClamAV
providers, generated test configuration, a one-time preparation command, and
restricted outbound networking. Verification and plan reports use `v1alpha6`;
all earlier report schemas remain loadable without rewriting their evidence.

This contract verifies one application in a disposable Kubernetes cluster. It
is not a Compose engine, a production migration runner, an arbitrary-code
sandbox, or proof of business transactions. The source image, command and
readiness contract still determine what the run can establish.

## Configuration

See `testdata/backend-http/cloudforge.yaml` for a complete runnable reference.

```yaml
schema_version: v1alpha5
safety:
  profile: bounded_backend
network:
  outbound: declared_dependencies_only
dependencies:
  postgresql: {enabled: true}
  redis: {enabled: true}
  clamav: {enabled: true, startup_timeout: 10m}
environment:
  DATABASE_URL: {from: dependency.postgresql.url}
  REDIS_URL: {from: dependency.redis.url}
  CLAMAV_HOST: {from: dependency.clamav.host}
  CLAMAV_PORT: {from: dependency.clamav.port}
  APP_TEST_KEY:
    generate: {bytes: 32, encoding: base64}
preparation:
  command: [node, prepare.js]
  timeout: 2m
runtime: {port: 8080}
endpoints: {readiness: /ready}
readiness:
  status: 200
  json: {status: ok}
```

Each binding selects exactly one of `from`, `generate`, or a bounded harmless
`value`. Generated values contain 16–64 random bytes encoded as hexadecimal or
base64. Values are generated only during execution, stored in run-owned Secrets,
and referenced from pods. No source `.env`, shell expansion, host secret import,
or production credentials are supported. Literal credential-like settings and
nonempty literal connection addresses are rejected.

`disabled.http_url`, `disabled.https_url`, `disabled.host`, and `disabled.email`
produce reserved `.invalid` destinations for an explicitly unexercised external
integration. They require restricted networking. They do not simulate a working
external provider or establish authentication, delivery, storage, or inference.

## Providers and preparation

- PostgreSQL 16.15 with pgvector 0.8.6 uses an immutable image, a generated
  disposable database password, SCRAM authentication, a bounded ephemeral volume,
  and an authenticated readiness query verifying both server and extension.
  The isolated test role is a superuser; it is not a recommended production role.
- Redis retains its existing internal, unauthenticated, disposable-test contract.
- ClamAV 1.5.4 uses an immutable image, seeded signatures, one bounded official
  signature update, then a daemon without a live updater. Readiness additionally
  requires the observed daemon engine/database/date tuple and signatures no more
  than 48 hours old. A successful local-version fallback is not daemon evidence.
  The actual signature version/date are recorded in the environment fingerprint.

Preparation is one Job using image A and direct executable arguments. It runs
once after dependencies and network restrictions are established and before the
application Deployment exists. There are no shell wrappers, inline scripts,
retries, source edits, business seeds, or automatic schema resets. The command
has 1–16 arguments, each at most 256 bytes; the whole-second deadline is 1s–5m.
The image's USER and working directory are preserved. Numeric/non-root identity
is not independently attested. Capabilities are dropped, privilege escalation
and service-account tokens are disabled, and the root filesystem is read-only
with a bounded writable `/tmp`.

A reliably observed nonzero exit or running command exceeding its bound is
`FAIL`; the dependent application is `BLOCKED`. Missing, ambiguous, or failed
observations are `ERROR`. Raw provider, application, and preparation logs are
not collected. Reports omit the command and literals while retaining hashes
for compatibility. Restoration revalidates providers and application readiness;
it does not rerun migrations, erase data, or promise schema rollback.

## Fixed capacity and network boundary

The existing default profile remains 4 CPU / 2 GiB aggregate workload capacity
and a 4 GiB cluster. Explicit `bounded_backend` allows 4 CPU / 5 GiB aggregate
workload capacity and a 6 GiB cluster. Both include providers, replicas, rollout
surge, and the separate preparation phase. Preparation is limited to 1 CPU /
512 MiB; PostgreSQL to 500m / 512 MiB; ClamAV to 1 CPU / 3 GiB. Replica and load
limits remain unchanged.

The backend profile requires a local Linux Docker daemon and an observable
capacity snapshot: at least 7 GiB total host and Docker memory, 6.5 GiB available
host memory, and 30 GiB available Docker storage. This is a preflight snapshot,
not a reservation against concurrent activity. Image A and B builds run before
the backend cluster starts; the 2 GiB builder is removed before allocating the
cluster. Remote Docker, Docker VM capacity and custom socket environments are
not inferred from the client machine.

Kubernetes egress policies deny undeclared pod traffic, allow cluster DNS and
same-run declared providers, and temporarily allow ClamAV-only public HTTP(S)
for signature updates. That allowance is revoked before preparation or app
startup. CloudForge reads back the complete policy set. The generic qualification
harness separately tests actual CNI filtering against a live controlled canary,
including positive controls and permitted provider connectivity.

These policies have Kubernetes node/host traffic exceptions and do not sandbox
hostile code. DNS is permitted. A policy readback is not proof that all possible
external destinations are unreachable. Builds still require network access to
retrieve source-declared packages. Only use trusted source on a disposable runner
for unfamiliar applications; the product does not modify the user's kubeconfig.

## Qualification and evidence

`python3 scripts/pilot-backend.py <binary> <output>` performs static inspection
and repeatable plans without Docker. `--run` is restricted to disposable
GitHub-hosted runners and exercises the generic healthy/failure cases. The
backend workflow retains original CLI stdout, stderr, exit codes, strict reports,
secret-omission checks, policy readbacks, CNI observations, and cleanup results.

A healthy dependency setup does not force application availability to PASS.
Original failure evidence remains present after successful restoration, and
later independent experiments can still run. Historical reports are not
reclassified based on the new capabilities.
