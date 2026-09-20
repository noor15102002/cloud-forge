# Controlled topology qualification evidence

The bounded topology implementation merged through protected main in
[PR #43](https://github.com/noor15102002/cloud-forge/pull/43), revision
`d0092933790c90cc963318375f9143eeb05bb2c7`. No new provider or workload category
was added. The protocol and limitations are in [controlled topology](controlled-topology.md).

## Validation

All twelve checks passed on PR revision `26c8d152708a4fd95c475768939bcb1f9a1b91d2`:

- [CI](https://github.com/noor15102002/cloud-forge/actions/runs/35475121970): formatting, race tests, vet, lint, vulnerability check, Action checks and build.
- [Topology](https://github.com/noor15102002/cloud-forge/actions/runs/35475121905): source/default topology cases and explicit one/two-replica comparison.
- [Monorepo](https://github.com/noor15102002/cloud-forge/actions/runs/35475121964): selected build, semantic readiness, lifecycle, wrong context, cancellation and cleanup.
- [Runtime integration](https://github.com/noor15102002/cloud-forge/actions/runs/35475121977): healthy and planted lifecycle failures.
- [Dependencies](https://github.com/noor15102002/cloud-forge/actions/runs/35475122012): Node/Python Redis, semantic readiness failures and cancellation.
- [Pilot matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35475121985): five healthy trials per family, planted failures, external apps and interruption checks.

The [post-merge packaged Action](https://github.com/noor15102002/cloud-forge/actions/runs/35475996881)
passed with report producer commit exactly `d0092933790c90cc963318375f9143eeb05bb2c7`.
The trusted default-branch reporter [rerun](https://github.com/noor15102002/cloud-forge/actions/runs/35475506393)
accepted v1alpha5 and published the [PR report](https://github.com/noor15102002/cloud-forge/pull/43#issuecomment-5746089320).
Its initial pre-merge attempt rejected the newer schema, as the old trusted loader
was still on main; the trust boundary was preserved.

Local checks also passed. Govulncheck found no called or imported-package
vulnerabilities, while noting one vulnerability in an unused required module.

## Measured outcomes, including failed attempts

The first explicit generic pair on implementation commit `68a78ff` observed
281 ms of downtime during one-replica pod recovery. Two replicas had no failed
availability probes. In the final PR pair, both replica counts passed the
availability experiments. Scan warnings remained visible independently.
The harness did not force a one-replica failure or a two-replica pass.

An earlier [legacy topology run](https://github.com/noor15102002/cloud-forge/actions/runs/35475021659)
recorded `runtime_version_unavailable` in the generated one-replica case: the
Kubernetes version could not be observed after cluster readiness. CloudForge
reported ERROR, blocked application experiments, and cleaned up. The same code
passed the subsequent complete legacy suite. The original ERROR and its report
are retained; its precise infrastructure cause was not established. A successful
repeat does not turn that attempt into a pass.

The first [post-merge integration run](https://github.com/noor15102002/cloud-forge/actions/runs/35475995780)
also stopped while preparing its deliberately broken-rollout fixture: k3d image
import returned an error before the rollout could execute. The report recorded
`image_import_failed` as ERROR and the workflow's cluster cleanup check passed.
The failure log/report are retained; the precise import cause was not established.
The packaged monorepo run on that same merged revision passed, and the subsequent
full integration suite for PR #45 passed. These are distinct observations; the
later passes do not erase the failed infrastructure attempt.

These paired observations are not statistical significance or a production
availability guarantee. Ready minima are sampled; passing availability can
coexist with a sampled zero Ready count. Different topology fingerprints are
intentionally incompatible for automatic regression grading.

## Private qualification

Private source and detailed application evidence remain in the private pilot
repository. The private protocol uses unchanged source, the original Dockerfile
and build context, explicit test topology, identical per-pod resources and a
fixed semantic readiness contract. Ordinary BuildKit cache is used to keep
filesystem layers stable; run-ownership labels are excluded when comparing
application image contents. A/B tags exercise rollout mechanics, not changed-code
upgrade compatibility. Qualification requires retained outcomes, restoration,
source integrity and owned cleanup.

The controlled private pair completed on the frozen merged `d009293` verifier.
Both topologies retained availability FAILs; redundancy reduced replacement gaps
but did not remove every failed probe. Both cases passed build, scan, startup,
semantic readiness, all baseline restorations and cleanup. Application source,
raw measurements, image identities and the source-grounded backend audit are
retained privately. The temporary transfer credential was removed.

An earlier private attempt stopped with node disk pressure before completing
the pair. Its infrastructure ERROR and cleanup evidence remain retained. The
disposable hosted-runner harness now checks disk capacity before execution;
application source, verifier and resource limits were not changed for the pair.
This also exposed [#44](https://github.com/noor15102002/cloud-forge/issues/44): an
unobserved semantic-readiness child incorrectly said FAIL under an infrastructure
ERROR. [PR #45](https://github.com/noor15102002/cloud-forge/pull/45) qualifies future
unobserved semantic results as BLOCKED while preserving actual HTTP observations
and historical reports. The completed private pair is not rewritten or rerun
under a different verifier revision.

PR #45 passed all twelve checks on `c9e28a881206438173eb55dbde63bdf4ce01c4f9`
and merged as `2d729e2e8e1268ba4b0e569e34bb2b973283aa15`. Its
[CI](https://github.com/noor15102002/cloud-forge/actions/runs/35476784001),
[runtime reliability suite](https://github.com/noor15102002/cloud-forge/actions/runs/35476783967)
and [repeated fixture matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35476783968)
passed alongside Redis, monorepo and packaged readiness checks.

The backend audit established separate database/extension, schema-preparation,
configuration and production readiness prerequisites. It did not execute the
backend or authorize a new provider. PostgreSQL alone is not assumed sufficient;
the next slice requires a deliberate contract decision. The controlled topology
milestone is complete without expanding workload or provider scope.
