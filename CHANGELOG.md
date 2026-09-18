# Changelog

All notable changes will be documented here. CloudForge follows Semantic
Versioning once releases begin.

## Unreleased

- Pilot implementation: correct root/probe/ORM/port analysis, bounded regular-file reading, faithful supported workload settings, private runtime configuration and resource budgets.
- Explicit endpoint/load configuration, compatible environment fingerprints and optional controlled readiness/in-flight shutdown experiments.
- Runnable Node/FastAPI reference fixtures and disposable-runner pilot validation. See the pilot checklist for validation status.

- Added graceful shutdown and synthetic version A-to-B rolling deployment experiments under continuous HTTP traffic.
- Added bounded k6 load profiles with normalized throughput, error-rate, and latency evidence.
- Added CPU-based HPA scale-up observation with explicit missing-metrics skip diagnostics.
- Added canonical verification JSON, a versioned JSON Schema, concise terminal summaries, and pull-request-ready Markdown reports.
- Added strict explicit baseline files with deterministic status and normalized measurement comparison.
- Added separate regression, improvement, and unavailable sections to JSON, terminal, and Markdown reports.
- Added a composite Ubuntu verification action with pinned runtime tools and JSON/Markdown artifacts.
- Added a trust-separated workflow that validates untrusted report JSON before updating one bot-owned pull-request comment.
- Added `cloudforge report` for safe rendering of saved verification JSON without repository execution.
- Hardened verification preflight and cleanup so analyzer diagnostics survive, interrupted builds remain execution errors, partial clusters are removed, and cleanup failures do not starve later removals.
- Hardened the public Action with canonical workspace paths, strict artifact and run input validation, Node 24 action runtimes, isolated temporary directories, and installation-token-compatible PR comment ownership.
- Added a reproducible read-only compatibility matrix for pinned external Node.js, TypeScript, and Python repositories.

### Added

- Initial Go CLI with version, environment doctor, and repository analysis.
- Deterministic Node.js, TypeScript, Python, Dockerfile, and Kubernetes metadata analysis.
- Versioned JSON contracts, fixtures, documentation, and CI foundation.
- Docker-to-k3d verification with container-build and Deployment-readiness evidence.
- Cleanup guarantees for successful, failed, timed out, and canceled verification runs.
- Deterministic container and Kubernetes configuration findings with remediation and provenance.
- Bounded Trivy image scanning with normalized vulnerability findings.
- HTTP startup/readiness measurements and controlled pod recovery under continuous traffic.
