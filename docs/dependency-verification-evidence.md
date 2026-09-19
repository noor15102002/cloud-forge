# Dependency-aware verification evidence

The Redis milestone extends the existing runtime engine with explicit dependency
planning, restricted environment bindings, bounded semantic readiness, and
versioned dependency evidence. See [the contract](dependency-runtime.md) and
[ADR 007](adr/007-explicit-dependency-runtime.md).

## Generic acceptance

Implementation: [PR #33](https://github.com/noor15102002/cloud-forge/pull/33).
Merged revision: `25c99e9e9772a11610a32b396604304179321ce2`.
Final validated feature revision: `e3eefe83132e8c9e147b79b53a41639c4c0b7bf8`
(GitHub pull-request jobs test the corresponding merge ref).

- [Standard CI](https://github.com/noor15102002/cloud-forge/actions/runs/35410446337): formatting, race tests, vet, lint, vulnerability checking, Action validation and build.
- [Packaged Action](https://github.com/noor15102002/cloud-forge/actions/runs/35410446355): healthy and planted-failure runtime cases.
- [Redis runtime and interruption](https://github.com/noor15102002/cloud-forge/actions/runs/35410446368): all cases below passed their expected assertions for Node and FastAPI.
- [Reference repetitions, external apps and interruption](https://github.com/noor15102002/cloud-forge/actions/runs/35410446357): five healthy trials per family, intended broken-fixture failures, two pinned public apps, four additional cancellation stages.
- [Post-merge Action run](https://github.com/noor15102002/cloud-forge/actions/runs/35411346901): passed on the merged revision.
- [Trusted PR reporter](https://github.com/noor15102002/cloud-forge/actions/runs/35410826696): v1alpha2 artifact rendered successfully after the trusted default-branch renderer was upgraded by the merge.

| Case | Validated observation |
| --- | --- |
| Healthy Node + Redis | Redis, deployment, semantic readiness, controlled readiness/shutdown, recovery, rollout and load PASS |
| Healthy FastAPI + Redis | Same supported runtime checks PASS |
| HTTP 200 / degraded | Dependency PASS, semantic and application readiness FAIL |
| Disconnected application | Dependency PASS, application readiness FAIL |
| Redis startup deadline | Dependency FAIL, run BLOCKED, application experiments SKIPPED |
| Redis startup cancellation | ERROR, owned resources removed, unrelated state preserved |
| Resource budget exceeded | BLOCKED before execution |
| Unsupported required dependency | BLOCKED before execution |

Security findings remain separate. A fixture with passing runtime checks may
have overall WARN because Trivy finds vulnerable packages in its image. No scan
warning is hidden to make a runtime test pass.

The strict report loader and repeated rendering verify v1alpha2 schema validity
and deterministic serialization. Legacy v1alpha1 files remain readable; baseline
comparison rejects incompatible schema/dependency environments. Unit tests also
cover malformed/bounded JSON responses, missing keys, forbidden environment
bindings, secret omission, API errors, cancellation and cleanup.

## Corrections found during validation

A k3d tools-mode import could return success while the image was unavailable in
the node. CloudForge now waits for cluster readiness, uses direct import mode and
confirms the image through the node CRI. Unavailable test images are execution
errors, without an application startup finding. Public fixture import diagnostics
are bounded and exclude credential retrieval and application logs.

A final canceled HTTP request could erase the previously observed response code.
Readiness measurements now retain the last HTTP response while semantic failure
remains FAIL. Redis rollout deadlines are also distinguished from tool/API access
errors instead of attributing every nonzero kubectl exit to the dependency.

## Limits

Redis is the only supported dependency provider. Dependency-loss disruption,
PostgreSQL and authenticated POST load profiles remain deferred. Response-body
assertions do not change Kubernetes probe semantics. Controlled Service gating
and targeted in-flight shutdown require the explicit application protocol.

Timing comparisons remain observations; this milestone does not establish a
statistically calibrated performance threshold or certify production readiness.
The pinned Redis index digest is recorded, but its image is not independently
scanned by the application Trivy step.

## Controlled private validation

The private pilot runs separately from public CI. Its source, configuration and
application findings are retained privately; public artifacts contain only the
generic reference fixtures. The controlled rerun reached build, dependency startup and semantic readiness.
It retained an observed availability failure and explicitly skipped unsupported
or unexecuted experiments. Source immutability, versioned report validation,
owned cleanup and unrelated-state checks passed. Private application results
are not a public benchmark or a claim of production readiness.
