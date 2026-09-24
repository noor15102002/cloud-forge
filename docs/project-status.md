# Project status — September 24, 2026

CloudForge is a working pre-deployment verification product, but the first public
prerelease remains **NO-GO**. The core analyzes Node.js, TypeScript and Python
workloads, builds and scans images, provisions disposable Kubernetes, tests
readiness and lifecycle behavior, restores state, cleans owned resources and
produces evidence-oriented reports.

The latest completed PR-development qualification, Attempt 4, recorded 64 cases:
59 passed their declared qualification contracts, 3 failed and 2 were incomplete.
These are verification cases, not 64 applications. It used a development binary
from synthetic PR merge `64632847c7e88a9d4492c7b26ebe7a4c9d758fba`; it is not
installed-archive proof for `v0.1.0-alpha.1`.

| Retained observation | Scope and next gate |
| --- | --- |
| Docker server-version observation timed out before build cancellation | Stable-core blocker: actual build cancellation must be exercised; historical cause remains unknown. |
| k3d image import failed in generated topology | Stable-core blocker: the runtime case must execute; historical cause remains unknown. |
| Fault fixture omitted image-version propagation | Harness defect corrected in `de6a808d20b44f2079dcd5454fab80073b6917dd`; corrected contract requires runtime qualification. |
| HPA desired four replicas but only two became Ready | Preserve the experimental FAIL and successful restoration/cleanup; this does not independently block HTTP-core release. |
| Worker fixture self-test timed out before CloudForge | Preserve experimental INCOMPLETE; baseline/metadata ordering was corrected, not the unknown timeout cause. Shared execution or cleanup problems still block. |

[PR #55](https://github.com/noor15102002/cloud-forge/pull/55) closed automatically
when its head branch was renamed. Its commits, discussions and all failed attempts
remain historical evidence. Release work continues on `work/release-hardening`
through a replacement PR; it has not yet merged.

Attempt 5 is a new, bounded development attempt for build cancellation,
generated topology, the corrected cleanup fault and the experimental malformed
worker fixture. It does not retry HPA for a preferred verdict, expand product
scope, change application code, increase deadlines or waive cleanup requirements.
The original attempts remain unchanged.

The automatic `test` and `readiness` checks remain required by protected main.
Broader runtime suites are explicit manual qualifications. The HTTP-core profile
copies public fixtures, omits HPA and optional control experiments, and records
original/effective hashes and every configuration change. Application behavior,
build files, resources and load limits remain unchanged. Core qualification
requires normal successful native results and explicitly unexecuted experimental
experiments; an arbitrary native FAIL is never accepted as a healthy core run.

There is no public CloudForge binary release. After the remaining core cases and
replacement PR are qualified, the exact merged main commit must produce one
canonical Linux/amd64 archive. Those exact installed bytes, their checksums,
identity, Action integration and cleanup must qualify before a release GO.
See [release qualification](releasing.md) and [support boundaries](supported-applications.md).
