# CloudForge v0.1.0-alpha.1 — candidate release notes

**Publication on hold: environment-hardening candidate qualification pending.**
This is a draft for the first public alpha, not a GO decision or a claim that
assets are published. The previously qualified archive from `977d863e` remains
historical evidence; its qualification does not transfer to changed bytes.
The final release record must identify the exact qualified commit, date, archive
and binary checksums, qualification run URLs and every failed attempt.

CloudForge is a local-first pre-deployment verifier for supported cloud-native
applications. This first public prerelease targets the **Linux/amd64 bounded
HTTP/Redis core**. It produces evidence about tested behavior; it does not certify
general production readiness or application business flows.

The candidate hardens the rule that successful verdicts
require trustworthy evidence. Invalid or mismatched scanner observations and
unusable cleanup observations produce operational ERROR. Application requirement
failures remain visible through restoration and cleanup. Environment/build and
observation failures are separated from application FAIL.

Reporting uses bounded scan summaries, safe sampled HTTP failure categories,
readable Markdown and strict external JSON decoding. Numerical comparisons are
advisory and experimental; historical reports are not rewritten or regraded.
The same-source rollout and sampled availability limits are explicit.
Runtime preflight, builds, scanning, cluster management and cleanup use the same
supported local Docker endpoint. The scanner policy is explicit and isolated
from ambient settings and ignore rules. The doctor includes system Buildx and
the shared compatibility policy. Planning remains runtime-independent and shows
capability maturity separately from whether an experiment can be scheduled.

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

## Publication record to complete after the exact archive qualifies

Record the qualified full source commit, version, platform, archive SHA-256,
contained binary SHA-256, qualification run URL, declared-contract counts, native
invocation count, durable evidence URL and hashes, public release URL, and hashes
of the assets downloaded again after publication. The annotated release tag must
identify the qualified commit even if documentation later advances `main`.
Upload the retained files byte-for-byte; do not rebuild during publication.

Use this evidence wording with the actual candidate's counts and links:

> The exact published archive was qualified before publication. All declared
> qualification contracts completed successfully. Native findings, including
> measured availability failures and deliberate fault/cancellation outcomes, were
> preserved unchanged. A successful qualification contract does not mean that
> every native application experiment passed.

Do not copy the earlier archive's 46/46 jobs, 44/44 contracts or 63 native
invocations into the replacement candidate's record. Keep its failed attempts,
cleanup results and exceptions visible. Mark the GitHub release **Prerelease /
alpha** and attach durable qualification evidence so public proof does not rely
only on expiring Actions artifacts.
