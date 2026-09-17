# Roadmap and Backlog

This document is the local source of truth until GitHub project write access is
configured. Each milestone contains coherent work intended to become GitHub
issues rather than an exhaustive task list.

## v0.1 — Core CLI

- [x] Establish repository, CI, documentation, shared models, and safe command execution.
- [x] Implement `version`, `doctor`, and deterministic Node/Python analysis.
- [x] Analyze root Dockerfiles and plain Deployment, Service, and HPA resources.
- [ ] Build the Docker-to-k3d readiness vertical slice with cleanup guarantees.
- [ ] Add static container and Kubernetes findings with Trivy integration.

## v0.2 — Runtime Experiments

- [ ] Add health/readiness and pod recovery experiments.
- [ ] Measure graceful shutdown and rolling deployment behavior under traffic.
- [ ] Integrate deterministic k6 load testing and HPA observation.

## v0.3 — Regression Engine

- [ ] Stabilize the verification evidence schema and JSON report.
- [ ] Load explicit baseline files and compare measurements and statuses.
- [ ] Render terminal and Markdown regression reports.

## v0.4 — GitHub Action

- [ ] Package and execute CloudForge in an Ubuntu action.
- [ ] Upload reports and safely obtain configured baseline artifacts.
- [ ] Update one marker-identified PR comment with minimum permissions.
- [ ] Document the fork and untrusted-code security model.

## v1.0 — Public V1

- [ ] Validate representative healthy and intentionally broken fixtures.
- [ ] Test several external repositories without repository-specific behavior.
- [ ] Harden cleanup, diagnostics, interruption, and unsupported-stack behavior.
- [ ] Produce reproducible release binaries and checksums.

