# CloudForge

CloudForge is an open-source, local-first pre-deployment verifier for supported
cloud-native applications. It produces bounded, reproducible evidence about how
one selected application behaves in a disposable Kubernetes environment.

CloudForge reports `PASS`, `WARN`, `FAIL`, `BLOCKED`, `SKIPPED` and `ERROR` explicitly.
A successful configuration check is not a production-readiness certificate, and
missing or unusable runtime observations must not become affirmative success.

The v0.1.0-alpha.1 prerelease targets **Linux/amd64 and one containerized HTTP
service** written in Node.js, TypeScript or Python, including selected monorepo
workloads and explicit Redis. See the authoritative
[current support and maturity table](docs/supported-applications.md).
PostgreSQL/pgvector, preparation, ClamAV, workers, controlled tests, HPA and numerical
regression grading remain experimental. See [release qualification](docs/releasing.md)
for the candidate gate and retained evidence requirements.

**[v0.1.0-alpha.1 is available as a prerelease](https://github.com/noor15102002/cloud-forge/releases/tag/v0.1.0-alpha.1).**
The published Linux/amd64 archive comes from exactly
`90f1c3c1560d4360b8ec90806154f65ea3d3d5a0` and passed
[installed-archive qualification](https://github.com/noor15102002/cloud-forge/actions/runs/36069870214):
47/47 declared contracts across 70 native CloudForge invocations. Deliberate
application and operational faults retain their original `FAIL`, `BLOCKED` and `ERROR` outcomes;
qualification success does not turn those observations into application success.
The published assets were downloaded again and their hashes verified.
See [project status](docs/project-status.md) for exact identities, preserved
failed attempts and the next independent developer pilot.

## First run

Follow the [first-run guide](docs/first-run.md) for the complete Linux/amd64
archive installation, checksums, Docker Engine and system Buildx prerequisite,
pinned tool installer and one small [HTTP-core example](examples/http-core).
The example has two replicas, readiness and bounded GET load; it does not enable
experimental HPA, control protocols, workers or backend preparation.

Runtime verification requires the supported local Docker socket plus Buildx,
k3d, kubectl, k6 and Trivy. `doctor` checks availability and compatibility;
`analyze` and `verify --plan` remain independent of installed runtime tools.
The [GitHub Action guide](docs/github-action.md) provides complete verification
and separate trusted pull-request reporting workflows for another repository.

For development, clone the repository and run `make build` with Go 1.27.1.
That source build is a development binary, not the qualified distribution.
`go install ...@latest` is not a supported release installation path because it
can omit the source identity required by runtime verification. The identity guard
remains enabled. `make release` builds a candidate from a clean committed checkout.

## Commands

```console
cloudforge version
cloudforge doctor
cloudforge analyze .
cloudforge analyze ./services/api --format json
cloudforge verify . --plan
cloudforge verify .
cloudforge verify ./services/api --format json
cloudforge verify ./services/api --format markdown
cloudforge verify ./services/api --baseline ./baseline.json
cloudforge report ./verification.json --format markdown
```

`analyze` reads supported repository metadata without executing application code.
For a monorepo, pass the repository boundary and select one workload, Dockerfile
and build context in [build configuration](docs/build-selection.md):

```yaml
schema_version: v1alpha3
build:
  app: apps/http
  dockerfile: apps/http/Containerfile.release
  context: .
```

`analyze . --config pilot.yaml` and `verify . --plan --config pilot.yaml` inspect
that selection. `verify . --config pilot.yaml` uses the same selected build inputs
for its lifecycle images.

Verification builds image A, scans that image with Trivy, creates an isolated k3d
cluster, provisions declared test dependencies and measures readiness, lifecycle
behavior and configured GET load. Image import uses a private local archive capped
at 4 GiB per image, targets an owned immutable node ID, verifies Docker-to-CRI
image identity and cleans its staging files; it does not retry a failed transport.
It records safe HTTP failure categories,
request counts, sampling intervals and final health. A run with no failed probes
cannot exclude shorter interruptions between samples. `downtime_ms` retains its
historical meaning: the maximum sampled failure window, not a continuously
measured outage duration.

A **same-source rollout** builds image B from the same selected checkout under a
new image reference/build. Keep that checkout unchanged until the run finishes:
CloudForge reads it again for each build and does not freeze a source snapshot.
It exercises rollout mechanics, not compatibility between
two independent application releases. Image A's scan does not establish image B's
security state. A failed experiment remains visible after baseline restoration;
restoration does not reset arbitrary business data. Owned cleanup is attempted
after success, failure, timeout and cancellation. Use `--keep-environment` only
for explicit manual inspection and follow the [retained-resource cleanup guidance](docs/security.md).

## Evidence and trust

JSON is the canonical `v1alpha8` report. The CLI and trusted GitHub reporter accept
legitimate historical `v1alpha1` through `v1alpha8` reports without adding missing
historical observations. Malformed documents and duplicate object keys are
rejected. See [reporting](docs/reporting.md) for status, display and schema details.

Baseline status regressions remain separate from the current run's absolute
findings. Numerical comparisons are experimental and advisory; a fixed 10%
tolerance does not establish statistical significance. Historical reports and
comparisons are never regraded during loading.

Verification executes Dockerfile instructions and application code. Use trusted
repositories, disposable runners, test credentials and test data. Runtime egress
policies have Kubernetes infrastructure/node/host exceptions and do not sandbox
Docker builds or hostile code. Never supply production credentials or data.
See [security](docs/security.md), [runtime configuration](docs/runtime-configuration.md)
and the [GitHub Action trust model](docs/github-action.md).

## Development and evidence

Use `make check` for tests, race checking, vet, lint, vulnerability checks, reporter
checks and builds. The [testing guide](docs/testing.md) describes generic fixtures,
intentional failures, cancellation and preservation of every qualification attempt.
[Architecture](docs/architecture.md) describes the existing execution boundaries.
[Roadmap](docs/roadmap.md) records historical milestones; it is not the current
support contract. Private application evidence is not part of the public release
qualification matrix.
