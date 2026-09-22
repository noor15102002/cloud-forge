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

## Install the qualified prerelease

Use the Linux/amd64 archive from the
[v0.1.0-alpha.1 release](https://github.com/noor15102002/cloud-forge/releases/tag/v0.1.0-alpha.1)
after its qualification record is available. The release contains the exact
qualified archive, SHA-256 checksum, and `release.json` with version, full source
commit, build date and binary checksum. An unpublished candidate is not yet a
qualified public release.

```sh
version=v0.1.0-alpha.1
archive="cloudforge_${version}_linux_amd64.tar.gz"
base="https://github.com/noor15102002/cloud-forge/releases/download/$version"
curl -fLO "$base/$archive"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/release.json"
sha256sum --check checksums.txt
tar -xzf "$archive" cloudforge
install -Dm755 cloudforge "$HOME/.local/bin/cloudforge"
"$HOME/.local/bin/cloudforge" version
"$HOME/.local/bin/cloudforge" doctor
```

Ensure `$HOME/.local/bin` is on your `PATH`. Compare the printed version and full
commit with the release record. Runtime verification also needs Docker, k3d,
kubectl, k6 and Trivy; the [Action](docs/github-action.md) installs the pinned
qualified tool versions. Repository analysis alone does not require those tools.

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
behavior and configured GET load. It records safe HTTP failure categories,
request counts, sampling intervals and final health. A run with no failed probes
cannot exclude shorter interruptions between samples. `downtime_ms` retains its
historical meaning: the maximum sampled failure window, not a continuously
measured outage duration.

A **same-source rollout** builds image B from the same frozen source under a new
image reference/build. It exercises rollout mechanics, not compatibility between
two independent application releases. Image A's scan does not establish image B's
security state. A failed experiment remains visible after baseline restoration;
restoration does not reset arbitrary business data. Owned cleanup is attempted
after success, failure, timeout and cancellation. Use `--keep-environment` only
for explicit manual inspection.

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
