# Project status — 2026-09-24

**Publication: [v0.1.0-alpha.1 prerelease](https://github.com/noor15102002/cloud-forge/releases/tag/v0.1.0-alpha.1).**
The qualified scope is the bounded Linux/amd64 HTTP/Redis core: analysis and
single-workload selection, Docker build and image-A scan, disposable Kubernetes,
readiness, lifecycle observations, bounded GET load, restoration, cleanup,
evidence reporting and the GitHub Action. Experimental capabilities remain
outside this release qualification.

## Published archive identity and custody

| Item | Verified value |
| --- | --- |
| Version / platform | `v0.1.0-alpha.1` / Linux/amd64 |
| Exact source and tag commit | `90f1c3c1560d4360b8ec90806154f65ea3d3d5a0` |
| Canonical qualification | [Run 36069870214](https://github.com/noor15102002/cloud-forge/actions/runs/36069870214) |
| Workflow jobs | 49/49 |
| Declared qualification contracts | 47/47 |
| Native CloudForge invocations | 70 |
| Preserved native outcomes | 21 WARN, 12 FAIL, 7 BLOCKED, 30 ERROR |
| Public archive SHA-256 | `f3220070f3c6e7f707914d257e37340b251eb68e475d051e9d9506fd70df1c6d` |
| Contained binary SHA-256 | `bdcad6b55b8ae0d0c38cff7e3742cc2c95dff9d6de327e0a177a65b8c5c036d5` |
| Public `checksums.txt` SHA-256 | `3a93ae9834f267418c3dcab8a96221a23540e1eced8e6ff1ac4ef7b99cd45ddf` |
| Public `release.json` SHA-256 | `a13708ddb68f35c40d603cd20b73dd6612f7d0d5319737fce267dcc7cceef729` |

These are the qualified retained bytes, published without rebuilding and then
downloaded again from the public prerelease. The release tag and manifest were
checked against the same source commit, version and platform. Later documentation
commits do not become the binary's qualified source.
The durable [publication custody record](https://github.com/noor15102002/cloud-forge/releases/download/v0.1.0-alpha.1/PUBLICATION.json)
and [qualification record](https://github.com/noor15102002/cloud-forge/releases/download/v0.1.0-alpha.1/QUALIFICATION.md)
are attached to the public prerelease.

Qualification contracts test expected behavior, including deliberate faults.
Native `FAIL`, `BLOCKED` and `ERROR` results remain preserved; successful harness
assertions do not convert them into `PASS`. Independent teardown does not change
native cleanup failures. This release proves only the behavior and conditions
recorded in its evidence, not general production readiness or business flows.

The release includes fixed scanner policy isolation, supported local Docker
endpoint enforcement, honest Buildx `NOT_VALIDATED` reporting, the core first-run
example and explicit maturity labels. Local image import streams one private
archive with a 4 GiB cap, uses the owned node's immutable ID, verifies the exact
Docker-to-CRI image identity, and performs bounded staging cleanup. It does not
retry the formerly failing k3d transfer path.

## Earlier replacement archives remain unpublished

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

A [focused follow-up run](https://github.com/noor15102002/cloud-forge/actions/runs/36060254004)
at `7e772ae003129f425a51069136514d631aa5f513` exposed a second import failure:
the staged k3d importer logged an inner containerd transfer EOF yet exited zero.
CloudForge correctly rejected that false success. All four Python cases were
attempted after confirmed cleanup; the original healthy-case ERROR remained,
and the later three expected outcomes were retained. Node Redis and both
cancellation contracts passed. The focused run remains **NO GO**. Available
records do not establish an OOM, daemon restart, disk-pressure or exact internal
transfer cause.

The published correction uses the pinned runtime's explicit local archive
importer described above. The Redis harness preserves early execution errors,
continues independent cases only after confirmed cleanup, and stops scheduling
on cancellation or cleanup uncertainty. The later successful qualification does
not establish the exact internal cause of an earlier transfer error and does not
regrade either failed archive or the focused follow-up run.

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
documentation commit does not become the qualified binary source. That archive
was never publicly released. Those bytes remain historical evidence and are not the
published environment-hardened alpha.

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

The next step is a small independent onboarding pilot with 3–5 developers using
the published archive and first-run guide. That pilot is still pending: record
installation/prerequisite friction, whether the support boundary is understood,
the usefulness of evidence and cleanup behavior. Qualification on prepared
runners does not establish effortless setup on every developer machine.

No private Peaxis application was changed or executed during this qualification.
The alpha remains scoped to the published [support boundaries](supported-applications.md);
new workload categories and dependency providers are not part of this release.
See [release qualification](releasing.md) for the retained proof requirements.
