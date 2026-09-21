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

- [x] Runtime/tool compatibility, explicit supported/unsupported/not_validated policy, and verifier identity.
- [x] Topology-aware availability evidence with source/generated provenance.
- [x] Bounded baseline restoration and continuation without erasing failed evidence.
- [x] Planner explanations separated from completed evidence.
- [x] Final full runtime, same-code topology and cancellation/cleanup matrix.

See the [validated acceptance record](evidence-reliability-evidence.md).
No new provider, workload category or private application pilot is included.

## Single-workload build selection

- [x] Select app metadata, custom Dockerfile and build context within one repository boundary.
- [x] Preserve deterministic plans, source provenance, A/B consistency and old configuration/report behavior.
- [x] Add generic workspace fixtures and safety/contract regression tests.
- [x] Validate the packaged Action, shared-package runtime, wrong context and cancellation on disposable runners.

Controlled private pilot execution and application-specific decisions are tracked
in the private pilot repository; source and detailed results are not published here.

See the [acceptance record, including the retained failed observation](build-selection-evidence.md).

Deeper backend dependencies require a deliberate follow-up decision. New providers,
richer HTTP workloads and prerelease packaging remain separate work.

## Release qualification: controlled topology

Tracking: [#42](https://github.com/noor15102002/cloud-forge/issues/42), [milestone 9](https://github.com/noor15102002/cloud-forge/milestone/9).

- [x] Implement bounded explicit test topology and provenance.
- [x] Add safety, source-preservation, schema and deterministic planning tests.
- [x] Complete disposable generic comparison and cancellation acceptance.
- [x] Freeze the private same-revision one/two-replica comparison evidence.
- [x] Refine the backend boot/readiness contract before deciding on PostgreSQL/pgvector.

See [the controlled protocol](controlled-topology.md) and
[qualification evidence, including retained failed attempts](controlled-topology-evidence.md).
No new provider is included. The private pair retained availability failures in
both topologies, with successful restoration and cleanup. Backend execution and
the choice of its exact dependency/configuration contract remain separate work.

## Bounded backend verification qualification

Tracking: [#48](https://github.com/noor15102002/cloud-forge/issues/48),
[milestone 10](https://github.com/noor15102002/cloud-forge/milestone/10).

- [x] Implement generated disposable test configuration and strict versioned contracts.
- [x] Add pinned PostgreSQL/pgvector and ClamAV providers with observed readiness.
- [x] Add bounded image-A preparation, capacity preflight and runtime egress restrictions.
- [x] Preserve failures, provider/data fingerprints, restoration and historical reports.
- [x] Qualify generic backend healthy/failure/network/secret-omission cases on disposable runners.
- [ ] Qualify cancellation and cleanup during new provider/preparation phases.
- [ ] Merge all validated changes through protected main.
- [ ] Run separately recorded private HTTP workload pilots with exact CLI output.

See [the bounded backend contract](backend-runtime.md). Earlier milestone
boundaries remain frozen. Full business flows, workers, arbitrary multi-service
orchestration and new cloud backends are not implied by this HTTP capability.

## v1.0 — Public V1

- [x] Validate representative healthy and intentionally broken fixtures.
- [x] Test several external repositories without repository-specific behavior.
- [x] Harden cleanup, diagnostics, interruption, and unsupported-stack behavior.
- [ ] [Calibrate performance-regression thresholds](https://github.com/noor15102002/cloud-forge/issues/31) across repeated compatible runner environments; the current 10% timing heuristic is not statistical significance.
- [x] [Align the bundled kubectl/Kubernetes version pair](https://github.com/noor15102002/cloud-forge/issues/35) before expanding pilots.
- [ ] Produce reproducible release binaries and checksums.

## Bounded worker heartbeat qualification

Tracking: [#50](https://github.com/noor15102002/cloud-forge/issues/50),
[milestone 11](https://github.com/noor15102002/cloud-forge/milestone/11).

The implementation adds an explicit single-worker command and Redis-heartbeat
contract, sequential nonoverlapping recovery/image replacement, restoration and
versioned evidence. Its boundary is process liveness and heartbeat progress;
queue jobs, business workflows and overlapping worker topologies remain outside
this slice. See [the contract and limits](worker-runtime.md). Generic runtime
qualification must pass before a private application pilot is authorized to use it;
implementation alone is not runtime evidence.
