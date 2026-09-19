# Roadmap and Backlog

This document mirrors the public GitHub milestones and project at a durable,
reviewable level. Each milestone contains coherent work rather than an
exhaustive task list.

## v0.1 — Core CLI

- [x] Establish repository, CI, documentation, shared models, and safe command execution.
- [x] Implement `version`, `doctor`, and deterministic Node/Python analysis.
- [x] Analyze root Dockerfiles and plain Deployment, Service, and HPA resources.
- [x] Build the Docker-to-k3d readiness vertical slice with cleanup guarantees.
- [x] Add static container and Kubernetes findings with Trivy integration.

## v0.2 — Runtime Experiments

- [x] Add health/readiness and pod recovery experiments.
- [x] Measure graceful shutdown and rolling deployment behavior under traffic.
- [x] Integrate deterministic k6 load testing and HPA observation.

## v0.3 — Regression Engine

- [x] Stabilize the verification evidence schema and JSON report.
- [x] Render concise terminal and pull-request-ready Markdown verification reports.
- [x] Load explicit baseline files and compare measurements and statuses.
- [x] Add baseline regression sections to terminal and Markdown reports.

## v0.4 — GitHub Action

- [x] Package and execute CloudForge in an Ubuntu action.
- [x] Upload reports and safely obtain configured baseline artifacts.
- [x] Update one marker-identified PR comment with minimum permissions.
- [x] Document the fork and untrusted-code security model.

## Pilot readiness

- [x] Complete the [pilot-readiness gate](pilot-readiness.md) and publish real runtime evidence before testing Peaxis or Avylo.

## Dependency-aware application verification

Tracking: [#32](https://github.com/noor15102002/cloud-forge/issues/32),
[milestone 7](https://github.com/noor15102002/cloud-forge/milestone/7).

- [x] Implement explicit Redis declarations, bounded provider and safe test bindings.
- [x] Add semantic readiness, capability planning and versioned dependency reports.
- [x] Add generic Node/FastAPI fixtures and focused regression tests.
- [x] Pass disposable Redis integration, cancellation and cleanup gates.
- [x] Merge validated implementation through protected main.
- [x] Rerun the controlled private FastAPI pilot and record the resulting boundary.

See [validated evidence and limits](dependency-verification-evidence.md). Private
application source and detailed findings remain outside the public repository.

PostgreSQL, dependency-loss experiments and authenticated POST loads remain deferred.

## Evidence Reliability Milestone

Tracking: [#37](https://github.com/noor15102002/cloud-forge/issues/37),
[milestone 8](https://github.com/noor15102002/cloud-forge/milestone/8).

Scope is frozen to [four reliability improvements](evidence-reliability.md):

- [ ] Runtime/tool compatibility, explicit supported/unsupported/not_validated policy, and verifier identity.
- [ ] Topology-aware availability evidence with source/generated provenance.
- [ ] Bounded baseline restoration and continuation without erasing failed evidence.
- [ ] Planner explanations separated from completed evidence.
- [ ] Final full runtime, same-code topology and cancellation/cleanup matrix.

No new provider, workload category or private application pilot is included.

## v1.0 — Public V1

- [x] Validate representative healthy and intentionally broken fixtures.
- [x] Test several external repositories without repository-specific behavior.
- [x] Harden cleanup, diagnostics, interruption, and unsupported-stack behavior.
- [ ] [Calibrate performance-regression thresholds](https://github.com/noor15102002/cloud-forge/issues/31) across repeated compatible runner environments; the current 10% timing heuristic is not statistical significance.
- [ ] [Align the bundled kubectl/Kubernetes version pair](https://github.com/noor15102002/cloud-forge/issues/35) before expanding pilots.
- [ ] Produce reproducible release binaries and checksums.
