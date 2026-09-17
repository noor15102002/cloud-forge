# Testing

Fast tests cover repository detection, structured parsing, deterministic
serialization, command failure classification, output limits, timeouts,
doctor status, CLI streams, and exit codes.

Fixtures under `testdata` represent supported Node/TypeScript and Python
applications. They contain only metadata needed by the analyzer; they are not
runtime demonstration services yet.

Runtime integration tests will be introduced with k3d execution. They will run
separately from fast unit CI and must verify cluster cleanup after success,
failure, timeout, and cancellation.
