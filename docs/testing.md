# Testing

Fast tests cover repository detection, structured parsing, deterministic
serialization, command failure classification, output limits, timeouts,
doctor status, CLI streams, and exit codes.

Fixtures under `testdata` represent supported Node/TypeScript and Python
applications. The Node fixture is also a runnable, dependency-free HTTP service
used by the readiness integration test. A deliberately incomplete fixture
protects failed and warning finding behavior.

The separate runtime integration workflow builds CloudForge, provisions a real
k3d cluster, verifies the Node fixture, validates structured readiness
evidence and Trivy scan output, and fails if a CloudForge cluster remains. Unit tests inject the
command runner to cover build, readiness, cluster creation, cancellation, and
retained-environment paths without requiring local runtime tools. Parser tests
cover normalized and malformed Trivy output.
