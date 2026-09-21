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

Local formatting, race tests, vet, lint, builds and schema/history checks passed.
Govulncheck reported no called or imported-package vulnerabilities, with one
advisory in an unused required module. Protected merge and a packaged Action run
on the actual merged revision remain required before private pilots.
