# Bounded backend qualification evidence

The contract is tracked in [#48](https://github.com/noor15102002/cloud-forge/issues/48)
and implemented in [PR #49](https://github.com/noor15102002/cloud-forge/pull/49).
See [the runtime contract](backend-runtime.md) for its limits. This document
records generic qualification; it does not claim a private application has run.

## Observed backend results

The [backend matrix on September 21](https://github.com/noor15102002/cloud-forge/actions/runs/35623183277)
tested PR head `74034622aaea08de93cfacd48a2d8d89f6b6fe65`. The healthy report
identifies the actual GitHub PR merge revision
`21f41f0400b020a20dede7af0bfe52c3eaacd215`.

| Case | Native CloudForge result | Qualification |
| --- | --- | --- |
| Healthy backend | WARN: nine Trivy findings; preparation, dependencies, readiness, lifecycle, restoration and load PASS | Passed |
| Missing generated application key | FAIL for readiness; dependent experiments BLOCKED | Passed |
| Missing schema preparation | FAIL for readiness; dependent experiments BLOCKED | Passed |
| Preparation command exits unsuccessfully | Preparation FAIL; application BLOCKED | Passed |
| Interrupt observed ClamAV startup | Cancellation ERROR; application unexecuted; cleanup and unrelated state preserved | Passed |
| Interrupt observed preparation command | Cancellation ERROR; application unexecuted; cleanup and unrelated state preserved | Passed |
| PostgreSQL interruption, initially with unrelated ClamAV prerequisite | ClamAV startup FAIL before PostgreSQL; cleanup operation errors retained; final resource inspection clean | Not qualified |

The healthy run observed PostgreSQL 16.15 with pgvector 0.8.6, Redis 7.4.6,
and ClamAV 1.5.4 with database 28130 dated `2026-09-21T06:24:29Z`.
Availability probes recorded no failures or downtime during graceful replacement,
pod recovery or rollout. Sampled minimum Ready counts were one, one and two
respectively. These are observed outcomes, not guaranteed availability.

The live CNI canary had successful positive controls before and after the denied
traffic tests. Application, preparation and ClamAV pods could not reach the
undeclared private canary; declared PostgreSQL, Redis and ClamAV endpoints were
reachable. Public-update allowance revocation was checked by policy readback;
arbitrary public destinations were not exhaustively tested. Source hashes,
secret omission and cleanup checks passed in the completed cases.

## Retained failed attempts

The [initial backend matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35622008491)
qualified the three deliberate failure cases. Its healthy qualification correctly
stopped when the network canary died before its final positive control, even
though providers had started and the preceding filtering observations passed.
Reset TCP connections caused an unhandled socket error in the qualification
helper. A focused reproduction fails with the old helper; the corrected helper
survives resets and retains independent before/after positive controls. The
original ERROR report is not rewritten as a passing run.

In the second matrix, the PostgreSQL interruption never reached its target
because the preceding ClamAV provider did not become ready within ten minutes.
The report retained that FAIL, blocked application experiments and recorded
cluster deletion/remnant cleanup operation errors. Final independent inspection
found no owned resources, and the unrelated running container, network, volume
and kubeconfig were unchanged. The exact antivirus startup cause was not
established; no application or provider logs were collected.

The PostgreSQL cancellation fixture now omits the unrelated antivirus provider
and rejects any attempt to start preparation or the application if the intended
interruption is missed. The complete provider combination remains covered by the
healthy and preparation cases. Its separate runtime qualification must pass
before the milestone is complete. Historical failed attempts remain evidence.

The [following attempt](https://github.com/noor15102002/cloud-forge/actions/runs/35625485373)
on head `38a2c873f8fa63f6edc835eb4148b8e77e8c1f5c` stopped even earlier: the direct
k3d image importer exited 1 before provider startup. CloudForge retained ERROR,
blocked dependent experiments, and final cleanup and unrelated-state checks
passed. This is another unqualified interruption attempt, not PostgreSQL failure.
[Issue #51](https://github.com/noor15102002/cloud-forge/issues/51) tracks the
unconfirmed import cause. The public fixture harness now preserves bounded raw
import stream tails and exact arguments/exit or signal. It forwards the original
streams, performs no retry, and never records kubeconfig, provider or app logs.

Local formatting, race tests, vet, lint, builds and schema/history checks passed.
Govulncheck reported no called or imported-package vulnerabilities, with one
advisory in an unused required module. The subsequent protected merge and
qualification are recorded below; none of these generic runs is a private pilot.

## Completed backend matrix and subsequent cleanup finding

The [fourth backend matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35627106331)
qualified all seven cases on head `a455103b72b8eee153404bf293d120f03ec25280`.
The native reports identify actual PR merge revision
`3aefbad81690ccc370df616f4540e2d896068828`. The healthy result remained WARN for
nine scanner findings; all backend lifecycle and restoration evidence passed.
The deliberate application failures retained FAIL/BLOCKED, while all three
observed provider/preparation interruptions retained cancellation ERROR and
passed independent cleanup and unrelated-state checks. Successful imports in
this matrix do not establish the cause of the earlier import errors.

A separate [existing Redis cancellation check](https://github.com/noor15102002/cloud-forge/actions/runs/35627106534/job/106424049110)
on that same revision reached its intended interruption, but failed cleanup.
The native report retained builder removal failure, a cluster deletion timeout
and a cluster-remnant cleanup error. Independent inspection found the named
BuildKit container and its cache volume still present. The underlying Docker
failure was not captured, so its cause remains unconfirmed.

This exposed a missing builder fallback. The follow-up records an explicit
run marker and the builder's container-to-volume ownership before removal,
then permits bounded cleanup of only that recorded identity after cluster
teardown. A successful fallback preserves the original cleanup error. A
separate disposable test deliberately fails the first removal and must prove
actual final cleanup and preservation of unrelated resources. Public fixture
cleanup stream capture retains the real tool failure without collecting
application, provider or credential output.

## Cleanup follow-up and protected merge

The [final backend matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35629827447)
qualified all seven cases on head `b619c4d3114b5ae7adc2aa67593649b87cd82160`.
The reports identify PR merge revision `5a133faf7dc92246d5713faaeb008c182b13923a`.
The [Redis cancellation matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35629827462)
qualified both ordinary cancellation and the explicitly injected builder-removal
failure. The injected case withheld the first removal, recorded exit 70 with
`actual_command_executed: false`, retained native ERROR, and proved the owned
fallback removed the remaining resources without changing unrelated state.
Ordinary cancellation observed successful real builder and cluster removal.
Neither run establishes the cause of the earlier spontaneous Docker failure.

The first [topology job on this candidate](https://github.com/noor15102002/cloud-forge/actions/runs/35629827457/attempts/1)
could not observe the Docker version and returned ERROR before building. It
remains a failed execution attempt. A [fresh-runner repeat](https://github.com/noor15102002/cloud-forge/actions/runs/35629827457/attempts/2)
completed: one source replica retained availability FAIL, while two source
replicas and the generated single-replica case passed their measured runtime
requirements. Restoration and final cleanup passed in all three completed cases.

All twenty checks passed before [PR #49](https://github.com/noor15102002/cloud-forge/pull/49)
was squash-merged through protected main as
`581f792619207aa595333b1ac98494e974965473` on September 21, 2026.
[CI on that merged revision](https://github.com/noor15102002/cloud-forge/actions/runs/35631816102)
passed. The [packaged Action run](https://github.com/noor15102002/cloud-forge/actions/runs/35631816119)
also passed on that actual merged producer identity: healthy runtime evidence
passed with scan warnings, while the broken shutdown and broken rollout fixtures
retained their expected FAIL results. Restoration and the workflow's cluster
cleanup check passed. The subsequent worker slice strengthens that final
independent check to inspect owned Docker containers, images, networks and
volumes, including builder caches; cluster absence alone is narrower evidence.
