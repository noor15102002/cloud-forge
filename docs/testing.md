# Testing

Fast tests cover repository detection, structured parsing, deterministic
serialization, command failure classification, output limits, timeouts,
doctor status, CLI streams, and exit codes.

Fixtures under `testdata` represent supported Node/TypeScript and Python
applications. The Node fixture is also a runnable, dependency-free HTTP service
used by the readiness integration test.

The separate runtime integration workflow builds CloudForge, provisions a real
k3d cluster, verifies the Node fixture, validates structured readiness
evidence, and fails if a CloudForge cluster remains. Unit tests inject the
command runner to cover build, readiness, cluster creation, cancellation, and
retained-environment paths without requiring local runtime tools.
