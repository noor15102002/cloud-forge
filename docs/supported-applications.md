# Current support and maturity

This is the authoritative scope for the Linux/amd64 v0.1.0-alpha.1 prerelease.
A capability's planner disposition (`SUPPORTED`, `BLOCKED`, `SKIPPED`) describes
whether CloudForge can schedule it for the selected configuration. It does not
assign product maturity, certify an application, or predict its measured verdict.

**STABLE / QUALIFIED CORE** identifies the narrow release contract that must pass
candidate qualification. **EXPERIMENTAL** identifies implemented behavior outside
that stable promise. **UNSUPPORTED** identifies claims or workloads that CloudForge
does not provide. A prerelease is not a claim that all software defects are known.
Qualification status and the exact binary identity belong to the release record;
older milestone evidence does not qualify a newer binary.

| Maturity | Scope | Evidence and boundary |
| --- | --- | --- |
| STABLE / QUALIFIED CORE | Linux/amd64; one selected containerized HTTP service; Node.js, TypeScript or Python | Deterministic selection and bounded application/runtime observations; other operating systems and architectures are unqualified. |
| STABLE / QUALIFIED CORE | Monorepo app root, selected Dockerfile and independent build context | One selected workload; does not orchestrate a complete monorepo. |
| STABLE / QUALIFIED CORE | Docker image build; Trivy scan of image A | A usable supported scan bound to the expected image can PASS or WARN; unusable observation is ERROR. Scanner findings do not establish exploitability. |
| STABLE / QUALIFIED CORE | Disposable k3d; plain supported single-container Deployment and Service | Supported fields are adapted to isolated run-owned resources; arbitrary manifests are not applied. |
| STABLE / QUALIFIED CORE | Explicit Redis; startup/readiness and semantic HTTP readiness | A disposable pinned provider and bounded flat JSON assertions; readiness does not establish business transactions. |
| STABLE / QUALIFIED CORE | Sampled pod replacement availability, pod recovery and same-source rollout | Failed requests, safe failure categories, sample count/interval and final health; shorter interruptions between samples cannot be excluded. Image B reads the same selected live checkout again; keep it unchanged for the entire run. |
| STABLE / QUALIFIED CORE | Bounded configured GET load; restore-and-continue; cancellation and owned cleanup | Original experiment results survive restoration. Restoration checks the intended runtime baseline; it does not reset arbitrary business state. |
| STABLE / QUALIFIED CORE | Terminal, JSON, Markdown and GitHub Action | The released Action installs the qualified archive. Historical report schemas remain readable without synthesizing missing evidence. |
| EXPERIMENTAL | PostgreSQL/pgvector, application preparation/migrations and ClamAV | Existing isolated backend contracts and generic evidence remain available; no persistent database recovery, HA or migration rollback certification. |
| EXPERIMENTAL | Redis-heartbeat worker verification | One process's bounded liveness and sequential recovery; no business-job, queue delivery or exactly-once claim. |
| EXPERIMENTAL | HPA, controlled readiness and targeted in-flight shutdown | Implemented bounded experiments; remain experimental for this prerelease even when individual corrected cases pass. |
| EXPERIMENTAL | Numerical regression grading | Advisory fixed heuristics, including a 10% timing/throughput tolerance; not a statistically established performance regression. |
| EXPERIMENTAL | Compatible but not yet qualified runtime tool combinations | Planner/runtime compatibility is separate from release qualification. |
| UNSUPPORTED | Business flows, login, billing, tenant correctness, AI answer quality and worker business-job correctness | CloudForge does not certify application business behavior. |
| UNSUPPORTED | Arbitrary Compose/Helm execution, generic multi-service orchestration and cloud deployment backends | Recognition is not execution support. |
| UNSUPPORTED | Production credentials/data, persistent DB recovery/HA and migration rollback certification | Use disposable test credentials and data only. |
| UNSUPPORTED | Hostile-code sandboxing and continuous-availability guarantees | Docker builds execute trusted source; Kubernetes policy is not an arbitrary-code sandbox. |
| UNSUPPORTED | Other operating systems/architectures | The prerelease distribution and runtime qualification cover Linux/amd64 only. |

Recognition of a language or client library does not establish boot requirements.
Explicit configuration selects supported test requirements; unresolved required
dependencies produce BLOCKED. Use `cloudforge verify --plan` before executing code.
Start with [the HTTP-core first run](first-run.md) to avoid opting into experimental
fixtures. Local runtime execution supports the default Unix Docker Engine socket;
custom/rootless sockets, remote engines and Docker Desktop contexts are rejected.
See [runtime configuration](runtime-configuration.md), [release qualification](releasing.md),
[backend limits](backend-runtime.md) and [worker limits](worker-runtime.md).
