# v0.1.0-alpha.1 candidate release notes

**Preparation status: qualification pending.** This document describes the
candidate scope; it is not a GO decision or a claim that assets are published.
The final release record must identify the exact qualified commit, date, archive
and binary checksums, qualification run URLs and every failed attempt.

CloudForge's first narrow prerelease hardens the rule that successful verdicts
require trustworthy evidence. Invalid or mismatched scanner observations and
unusable cleanup observations produce operational ERROR. Application requirement
failures remain visible through restoration and cleanup. Environment/build and
observation failures are separated from application FAIL.

Reporting uses bounded scan summaries, safe sampled HTTP failure categories,
readable Markdown and strict external JSON decoding. Numerical comparisons are
advisory and experimental; historical reports are not rewritten or regraded.
The same-source rollout and sampled availability limits are explicit.

The qualified distribution target is Linux/amd64. The supported core is one
selected Node.js/TypeScript/Python HTTP service, including monorepo build selection,
explicit Redis, Docker image-A scan, disposable k3d, startup/semantic readiness,
sampled replacement/recovery/rollout, bounded GET load, restoration, cancellation,
owned cleanup and terminal/JSON/Markdown/Action evidence. See the authoritative
[support table](supported-applications.md) for exact scope and exclusions.

PostgreSQL/pgvector, preparation/migrations, ClamAV, worker heartbeats, HPA,
controlled readiness, targeted in-flight shutdown and numerical regression grading
remain experimental. No business-flow, exactly-once worker, persistent recovery,
continuous-availability or hostile-code sandbox guarantee is made.

Install the published archive after checking its SHA-256; do not use an unversioned
source install as the public release path. The released Action installs those same
qualified bytes and verifies the full source commit against the immutable Action
pin. Qualification and publication must follow [the release procedure](releasing.md).

Private application pilots and their historical evidence remain unchanged and are
not rerun as part of this prerelease.
