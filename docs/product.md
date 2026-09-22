# Product definition

CloudForge produces bounded, reproducible evidence about one selected supported
application's behavior under a disposable Kubernetes test configuration. It does
not certify production readiness or infer success when observations are missing.

The [current support and maturity table](supported-applications.md) is the single
authoritative scope. The Linux/amd64 prerelease core covers supported HTTP services,
Docker builds, image-A scanning, explicit Redis, readiness, sampled lifecycle
availability, same-source rollout, configured GET load, restoration, cancellation,
owned cleanup and terminal/JSON/Markdown/GitHub evidence.

A same-source rollout exercises new image references from the same selected source;
it does not validate compatibility between independent application releases.
Sampled probes cannot exclude interruptions between requests. Baseline restoration
checks runtime requirements and does not reset arbitrary business state. Scanner
findings are reported observations, not proof of exploitability. Numerical baseline
grades are experimental and advisory.

Backend providers, preparation, ClamAV, worker heartbeats, HPA and controlled
readiness/shutdown experiments remain experimental. Planner SUPPORTED means a
capability can be scheduled for this configuration; maturity is separate.

CloudForge does not execute arbitrary Compose/Helm topologies, deploy to cloud
backends, validate login/billing/tenant/business flows, grade AI answer quality,
certify worker jobs or provide an arbitrary-code sandbox. Verification executes
trusted Dockerfiles and application source using disposable test data.
