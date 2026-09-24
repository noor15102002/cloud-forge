# Project status — September 24, 2026

**Publication: ON HOLD pending qualification of the environment-hardening candidate.**
CloudForge analyzes supported Node.js, TypeScript and Python HTTP workloads,
builds and scans images, provisions disposable Kubernetes, tests readiness and
lifecycle behavior, restores state, cleans owned resources and produces evidence.
Selected monorepos and explicit Redis are included in the bounded core scope.

The earlier archive passed its declared qualification contracts in the prepared
runner environment. Subsequent review found ambient Trivy policy and Docker
endpoint-selection gaps at the developer-machine boundary. The current change
isolates and records the scanner policy, pins the selected supported Docker
endpoint, aligns doctor/Buildx prerequisites and labels experimental capabilities.
It also supplies a core-only first-run example and consumer reporter instructions.
A new archive must pass qualification before publication; the historical results
below remain unchanged and do not qualify new bytes.

## Replacement archives remain unpublished

[PR #58](https://github.com/noor15102002/cloud-forge/pull/58) merged the environment
hardening as `b0b5eb02a32637a0a1043b92472ca2cca8954327`. Its
[archive run](https://github.com/noor15102002/cloud-forge/actions/runs/36055293213)
completed with 46 of 47 contracts passing. The first-run example returned HTTP
503 from its readiness route during draining: recovery had 1/168 failed probes
and rollout 21/285. Those failures remain unchanged. [PR #59](https://github.com/noor15102002/cloud-forge/pull/59)
corrected the example and passed the unchanged zero-failure Action check.

The next [archive run](https://github.com/noor15102002/cloud-forge/actions/runs/36057389830)
uses merged source `3e2e00212a5c82e9c26777ed6127aa271a8a71f3`. The corrected
example passed, but the Redis Node case encountered a k3d direct-import
closed-connection error before application startup. Native ERROR and cleanup PASS
were retained. A harness assumption about missing dependency results then stopped
its final independent case, leaving that qualification contract INCOMPLETE.
This archive has **NO GO** and will not be published.

The follow-up stages imports through the owned image volume, independently checks
the exact imported identity and preserves logged errors. The Redis harness now
records all independent cases after proven cleanup, while cancellation or cleanup
uncertainty stops scheduling. These changes need protected checks and a new exact
archive qualification; neither earlier archive is a substitute.

## Historical qualified candidate (unpublished)

[PR #56](https://github.com/noor15102002/cloud-forge/pull/56) merged through
protected main as `977d863e94722bc236201d2deb16a856def7dee2`.
[Canonical qualification run 36044464022](https://github.com/noor15102002/cloud-forge/actions/runs/36044464022)
built the candidate once, reproduced it with a separate compiler cache, and
installed those same verified archive bytes for runtime and Action checks.

| Identity | Qualified value |
| --- | --- |
| Version and platform | `v0.1.0-alpha.1`, Linux/amd64 |
| Source commit | `977d863e94722bc236201d2deb16a856def7dee2` |
| Embedded source date | `2026-09-24T18:53:19Z` |
| Archive SHA-256 | `f8b6714b4feda36aa10a78f211527a4c5bd9aa54b47a51b4012554f123882d4a` |
| Binary SHA-256 | `732fe96c53d94a3d6b157d2da8a4bd133bb933a7e9ac53e3eca9100b6416ae01` |

All 46 workflow jobs passed. The 44 runtime qualification contracts cover 63
distinct native invocations: 59 in the generic matrix, two packaged Action runs,
one monorepo build-context rejection and one monorepo build cancellation.
Static checks, clean installation, reproducibility and live scoped reporting
also passed. Runtime records retain the exact candidate identity, original
status and exit, report hashes, assertions, cleanup and artifact outcomes.

All 137 artifact archives and the workflow log archive were retained and verified.
The 63 native outcomes were 1 PASS, 20 WARN, 10 FAIL, 2 BLOCKED and 30 ERROR.
Native cleanup was PASS in 54 reports, ERROR in eight deliberate cleanup-fault
reports, and not applicable in the Docker-unavailable case. All 44 independent
cleanup checks passed; these are separate observations.

A passing qualification contract can require a native FAIL, BLOCKED or ERROR
for an intentional application failure or operational fault. Those verdicts
remain unchanged. Independent teardown does not turn native cleanup ERROR into
PASS. Live reporting reused the retained cleanup-fault report and preserved both
the original application FAIL and cleanup ERROR; it was not another native run.

Qualification applies only to the archive and commit above. A subsequent
documentation commit does not become the qualified binary source. No public
binary release or release tag has been published. These bytes are retained as
historical evidence and will not be published as the environment-hardened alpha.

## Earlier attempts remain evidence

The earlier development Attempt 4 recorded 59 passing contracts out of 64,
with three failures and two incomplete cases, using synthetic PR merge
`64632847c7e88a9d4492c7b26ebe7a4c9d758fba`. It was not installed-release proof.
Its Docker server-version timeout and k3d image-import failure retain unknown
historical causes. A later successful case does not diagnose those old failures.
The experimental HPA FAIL and pre-execution worker INCOMPLETE remain unchanged.

[PR #55](https://github.com/noor15102002/cloud-forge/pull/55) closed when its head
branch was renamed; PR #56 replaced it without removing prior evidence.
The bounded [Attempt 5](https://github.com/noor15102002/cloud-forge/actions/runs/36043072069)
passed its three stable-core contracts and experimental malformed-worker
contract. It tested a development binary, not this release archive.

The [first replacement-PR Action attempt](https://github.com/noor15102002/cloud-forge/actions/runs/36043037282)
remains incomplete: generated qualification files dirtied the verifier checkout,
and its identity guard refused execution. A narrow ignore-rule regression fix
was followed by passing [CI](https://github.com/noor15102002/cloud-forge/actions/runs/36043387475)
and [readiness/Action checks](https://github.com/noor15102002/cloud-forge/actions/runs/36043387502).
The failed attempt was preserved, not regraded.

## Scope and next step

The explicit HTTP-core fixture profiles retain application/build bytes,
resources and load limits, with source/effective hashes and exclusions recorded.
HPA and optional readiness-gating/inflight-shutdown controls were excluded before
execution and must appear SKIPPED and unexecuted in those profiles.
PostgreSQL/pgvector, preparation, ClamAV, workers, controlled tests, HPA and
numerical regression grading remain experimental; this archive gate does not
qualify them. Shared execution, observation, identity, artifact and cleanup
defects remain release blockers regardless of which experiment exposes them.

The next step is exact-archive qualification of the environment-hardening change,
including hostile ambient scanner settings, rejected Docker endpoint selection
and the first-run example. Publication follows only after that gate, with a bounded
support statement, durable evidence and independently checked public downloads.
No private Peaxis application was
changed or executed during this qualification. See
[release qualification](releasing.md) and [support boundaries](supported-applications.md).
