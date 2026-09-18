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
validates structured evidence and Trivy scan output and fails if a CloudForge
cluster remains. Unit tests inject the command runner to cover build, readiness,
cluster creation, cancellation, and retained-environment paths without requiring
local runtime tools. Parser tests cover normalized and malformed Trivy output.

Runtime tests also cover readiness HTTP retries, successful replacement after
a temporary unready state, request failures during deletion, recovery timeout,
final health, graceful termination traffic, version rollout transitions,
broken shutdown and rollout fixtures, and cancellation-safe cleanup.
