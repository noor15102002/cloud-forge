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

- [ ] Complete the [pilot-readiness gate](pilot-readiness.md) and publish real runtime evidence before testing Peaxis or Avylo.

## v1.0 — Public V1

- [x] Validate representative healthy and intentionally broken fixtures.
- [x] Test several external repositories without repository-specific behavior.
- [x] Harden cleanup, diagnostics, interruption, and unsupported-stack behavior.
- [ ] Calibrate performance-regression thresholds across repeated compatible runner environments; the current 10% timing heuristic is not statistical significance.
- [ ] Produce reproducible release binaries and checksums.
