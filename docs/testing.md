# Testing

The [release candidate workflow](../.github/workflows/release-candidate.yml) is the
authoritative exact-binary gate: it builds one reproducible Linux/amd64 archive and
installs those bytes across every generic runtime case and the packaged Action.
Existing development workflows rebuild source and are not substitutes for that
gate. See [release qualification and failed-attempt retention](releasing.md).

The `explicit-topology` reliability job compares the unchanged monorepo fixture
under one and two configured replicas. Application PASS/FAIL remains measured;
the gate requires complete evidence, restoration, source integrity and cleanup.
Unit tests cover invalid/null/unknown fields, old-version rejection, source
preservation, HPA conflict, surge budgets, deterministic plans, versioned schemas,
baseline incompatibility and joined cancellation during readiness observation.

Fast tests cover repository detection, structured parsing, deterministic
serialization, command failure classification, output limits, timeouts,
doctor status, CLI streams, and exit codes.

Fixtures under `testdata` represent supported Node/TypeScript and Python
applications. The Node fixture is also a runnable, dependency-free HTTP service
used by the readiness integration test. A deliberately incomplete fixture
protects failed and warning finding behavior.

The separate runtime integration workflow builds CloudForge, provisions real
k3d clusters, verifies the healthy Node fixture, and confirms that the broken
shutdown and rollout fixtures fail for their intended behavioral reasons. It
validates structured lifecycle, load, HPA, and Trivy evidence and fails if a CloudForge
cluster remains. Unit tests inject the command runner to cover build, readiness,
cluster creation, cancellation, and retained-environment paths without requiring
local runtime tools. Parser tests cover normalized and malformed Trivy output.

The healthy Node fixture delays listener shutdown for two seconds after SIGTERM so
Kubernetes can remove the terminating endpoint before connections are closed.
The broken shutdown fixture exits immediately, while the broken rollout fixture
keeps synthetic version `b` unready.

Runtime tests also cover readiness HTTP retries, successful replacement after
a temporary unready state, request failures during deletion, recovery timeout,
final health, graceful termination traffic, version rollout transitions,
broken shutdown and rollout fixtures, and cancellation-safe cleanup. Deadline
regressions interrupt an in-progress rollout read, then separately cover an
observed unready state, late healthy state, final API timeout, malformed response,
and cancellation. A full verifier test preserves the resulting FAIL through
restoration, later load evidence, cleanup, and strict saved-report loading.

The k6 adapter tests valid and malformed summary exports. HPA tests use official
Kubernetes status types and injected runners to cover metrics availability,
replica observations, bounded scale-up, and explicit skip behavior.

Renderer golden files lock terminal and Markdown presentation. The JSON golden
test compares decoded values rather than raw bytes, so object key order is not
part of the contract; a separate repeatability test shuffles every collection
and expects identical canonical serialization. The versioned verification JSON
Schema is also parsed during tests.

Regression tests cover strict and bounded baseline loading, schema mismatch,
duplicate evidence, status ordering, metric direction, missing measurements,
and separation of current findings from relative changes. CLI tests confirm an
invalid baseline prevents execution and an evidence-status regression produces a full
report with exit status `1`. Numerical-only differences are advisory WARN and do
not convert successful verification to exit `1`; historical grades are retained.

GitHub integration tests execute the packaged composite action against the real
healthy fixture and continue to exercise the intentionally broken shutdown and
rollout fixtures with its built binary. Node's built-in test runner covers
marker validation, comment size limits, create-versus-update behavior, owner
checks, authentication fallback, and duplicate bot-comment cleanup. Shell tests
exercise valid and rejected Action inputs, including workspace containment,
artifact names, retention limits, run ID bounds, and token requirements.
Workflow and action YAML is parsed during CI before runtime integration.

Verification unit tests preserve analyzer findings and diagnostics through
preflight failures, distinguish application build failures from interrupted
CloudForge execution, and prove that each cleanup operation receives an
independent timeout so one failed deletion cannot prevent later cleanup.

Run `make external` to fetch the exact revisions listed in the
[external compatibility matrix](external-validation.md) and analyze them
without executing their code. This networked check is reproducible but is kept
outside the required pull-request CI path so upstream availability cannot block
local development.

The pilot workflow adds five repeated trials each for runnable Node and FastAPI
services, controlled readiness-gating and targeted in-flight shutdown evidence,
and planted Python readiness/shutdown failures. The healthy Python launcher
allows EndpointSlice removal to propagate before closing the Uvicorn listener.
Real interruption tests preserve an existing kubeconfig/context and unrelated
Docker container/network/volume while checking run-owned resource cleanup.
`metadata-node` and `metadata-python` remain compact analyzer-only fixtures;
`healthy-node` and `healthy-python` contain runnable reference applications.

## Redis and semantic readiness

`.github/workflows/dependencies.yml` runs `scripts/pilot-dependencies.py` against
`healthy-node-redis` and `healthy-python-redis` on disposable GitHub runners.
Each family covers a healthy dependency/application, disconnected application,
HTTP 200 semantic degradation and deterministic Redis startup timeout. The
harness delays Redis's readiness probe for the timeout case; this is recorded
test fault injection, not a product configuration capability.

`pilot-cancellation.py --stages redis` interrupts actual Redis startup and checks
owned cleanup, private kubeconfig removal and unrelated sentinel Docker state.
Unit tests cover invalid declarations, unsafe bindings, missing/duplicate/nested
JSON fields, bounded bodies, resource accounting, startup order, failure/cancel
classification, fingerprint omission and report schema loading.

Historical private application pilots were separate from generic qualification.
This prerelease task does not rerun them. Private source and evidence must not enter
public workflow artifacts. Dependency-loss disruption and richer HTTP workloads remain
unsupported rather than being claimed as tested.

Validated dependency-runtime results and downloadable artifacts are recorded in
[dependency verification evidence](dependency-verification-evidence.md).

## Evidence reliability matrix

`go test -race ./...` covers compatibility, identity, restoration and continuation
without runtime tools. `scripts/pilot-reliability.py` requires disposable runtime
tools and proves the same Node behavior under one/two source replicas and one
generated replica, with identical source hashes, persistent original failures,
later evidence, strict report round trips and cleanup. The
`.github/workflows/reliability.yml` job runs alongside the full existing matrix.

## Single-workload selection

`go test ./...` covers selected metadata/provenance, custom filenames, independent
contexts, path traversal, symlink and FIFO rejection, 2 MiB Dockerfile bounds,
A/B argument consistency, config placement, plan determinism and cancellation.
The Single-workload selection workflow uses the packaged Action against
`testdata/monorepo`, whose HTTP readiness imports a shared workspace package.
It requires successful A/B rollout and restoration, records a real build failure
with an app-only context, interrupts the custom Dockerfile build, checks source
hashes, and proves cleanup preserves a pre-existing container/network/volume and
kubeconfig. Reports and assertions are uploaded as separate artifacts.

## Backend qualification

The Bounded backend runtime workflow exercises `testdata/backend-http` with real
PostgreSQL/pgvector, Redis, ClamAV, preparation, failure cases, live CNI controls,
secret omission and cleanup. Its original CLI output is retained even on failure.
See [the backend contract](backend-runtime.md).

## Bounded worker qualification

The worker runtime workflow and `scripts/pilot-worker.py` exercise `testdata/healthy-worker` with advancing, missing, stale, frozen, malformed and future timestamps, container exit, a heartbeat produced only by the original pod, recoverable replacement failure, and cancellation during startup or replacement. Qualification preserves raw CLI output and requires actual owned pod identities, old-key expiry, fresh advancing samples, restoration and cleanup. A heartbeat result establishes process liveness; no fixture case proves business-job completion. Injected runner tests separately cover container restarts, post-import baseline checks, observer deadlines, provider fingerprint changes, safe serialization and historical report loading.

## Bounded HTTP probe pacing

The reliability workflow's `probe-pacing` job compares unchanged rate-limited
Python fixture bytes with omitted pacing and an explicit two-second interval.
The default case must preserve observed HTTP 429 failure evidence; the explicit
case must complete readiness, lifecycle observations and restoration without
429, while retaining any other measured availability FAIL. The harness records
original CLI streams/exit codes, deterministic plans, source and image filesystem
hashes, policy fingerprints and ownership cleanup. It also runs the existing
readiness-cancellation harness with fixed two-second pacing and unchanged-state
sentinels. Pure helper tests protect the fixture rate limit, evidence assertions,
and cancellation fixture guards. See [the pacing contract](probe-pacing.md).
