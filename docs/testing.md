# Testing

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
broken shutdown and rollout fixtures, and cancellation-safe cleanup.

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
invalid baseline prevents execution and a detected regression produces a full
report with exit status `1`.

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
