# Worker heartbeat qualification evidence

The bounded contract is tracked in [#50](https://github.com/noor15102002/cloud-forge/issues/50)
and [PR #52](https://github.com/noor15102002/cloud-forge/pull/52). See
[worker runtime](worker-runtime.md) for what heartbeat evidence can establish.
No private application execution is claimed by this record.

## First disposable matrix

The [first matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35628263293)
tested PR head `5df4e5c0a2b1b9afa78e8a73b1dd0983ed724855`; native reports identify
the actual PR merge revision `a8041fd9693d94afa7b329b6b53cf0af9abd8913`.
Nine of eleven scenarios qualified. All eleven retained cleanup and unrelated
resource checks; none left owned resources behind.

| Scenario | Native observation | Qualification |
| --- | --- | --- |
| Healthy worker | Startup, recovery, image transition and both restorations PASS; overall FAIL from an inappropriate HTTP-port finding | Not qualified |
| Never publishes | Heartbeat requirement FAIL; later worker experiments BLOCKED | Passed |
| Stale timestamps | Old advancing timestamps retained; heartbeat requirement FAIL | Passed |
| Frozen timestamp | One non-advancing timestamp retained; heartbeat requirement FAIL | Passed |
| Future or malformed heartbeat | Unreliable observation ERROR, not an application requirement verdict | Both passed |
| Process exit | Actual exit 23/Error and restart observed; startup FAIL | Passed |
| First pod alone publishes | Recovery FAIL, failed restoration, image transition BLOCKED | Passed |
| Second pod alone suppresses heartbeat | Fixture exited 24 during initial control setup; startup FAIL before the intended experiment | Not qualified |
| Startup and recovery interruption | Started experiment ERROR, later experiments unexecuted, cleanup passed | Both passed |

The healthy run's native evidence matched five distinct controller-linked pods
and the intended A/A/A/B/A image sequence. The overall FAIL was retained. Fresh
worker verification now marks HTTP port, HTTP probe and HTTP availability replica
requirements SKIPPED, with reasons and source provenance. Standalone analysis,
HTTP verification, security and resource findings remain unchanged. Historical
reports are not rewritten by this correction.

The second-start fixture initialized its Redis counter outside its retry loop.
The exact transport or command failure was not retained, so its exit is not
attributed to a specific Redis fault. Control claims now execute inside the
existing retry loop and atomically assign ownership/ordinal once per pod.
Each initial claim is repeated against real Redis before publication to prove
idempotent replies. This changes only the public reference fixture; CloudForge
does not manipulate that fixture's control key. The three-second heartbeat
expiry and one-hour control-key expiry remain unchanged.

## Corrected disposable matrix

The [corrected matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35631115367)
qualified all eleven scenarios on head
`decc924b99e996c1d353f8343b44e38423af1481`. Native reports identify PR merge
revision `76342a5928208ad21b88751df97df5c25480d4bd`.

- The healthy worker retained WARN for nine container-scan findings; startup,
  recovery, image transition and both restorations passed.
- Missing, stale and frozen heartbeats retained requirement FAIL. Future and
  malformed heartbeats retained observation ERROR. The exited process retained
  its observed failure.
- The first-pod-only fixture passed startup, failed recovery and failed baseline
  restoration. The image transition remained BLOCKED and unexecuted.
- The second-start fixture retained overall FAIL after the replacement missed
  its heartbeat deadline. Restoration passed, the later image-B transition
  passed, and the final image-A restoration passed. Native and independent
  observations matched five distinct pod/process identities in the A/A/A/B/A
  sequence. Successful restoration did not erase the original FAIL.
- Both interruption cases qualified with cancellation evidence and independent
  cleanup and unrelated-state checks.

The preceding failed attempts remain separate records. Passing this matrix does
not reinterpret their native FAIL or ERROR results.

## Other qualification findings

The [packaged Action on the same worker candidate](https://github.com/noor15102002/cloud-forge/actions/runs/35631115327)
built successfully but observed both HTTP fixture pods still `ContainerCreating`
at the readiness deadline, with zero Ready and 128 failed probes. Cleanup also
reported builder removal failure and cluster/remnant timeouts, so the native
result was ERROR/exit 2. The underlying container/runtime cause was not observed;
this is not attributed to an application HTTP handler. The final workflow check
proved only cluster absence. It now uses a bounded check of all owned Docker
containers, images, networks and volumes, including builder caches. That change
improves cleanup qualification; it does not claim to fix the unconfirmed cause.

The first backend missing-key job on this candidate stopped in its local network
helper test before CloudForge ran. A [repeat on the same revision](https://github.com/noor15102002/cloud-forge/actions/runs/35631115474/attempts/2)
qualified the expected native readiness FAIL/exit 1 with successful preparation,
providers and final cleanup. The pre-runtime helper failure remains retained.

## Final main-based qualification

The final candidate `55d35dbd1ed13e5552365d27266c8e6c5f0a2f4e` included the
protected backend merge without changing its already integrated code. All
31 checks passed before protected squash merge as
`7c5bd67b789293edf83d67d566715ccc481e886a`. The [worker matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35633634017)
qualified all eleven scenarios again, with native producer
`c4f2e361e7c7e21b2d8e557f086677a42b5d3ae2`. Healthy progress, original FAIL
preservation after restoration, blocked restoration, both cancellations and
independent cleanup matched the intended contract.

Three other first attempts remain retained separately from their single
unchanged-candidate repeats:

- The [observed Redis cancellation attempt](https://github.com/noor15102002/cloud-forge/actions/runs/35633634310/attempts/1)
  stopped at image import before its interruption target. Native ERROR and
  successful final cleanup were retained. Its repeat reached actual Redis
  startup, qualified cancellation and passed cleanup.
- The [backend missing-schema attempt](https://github.com/noor15102002/cloud-forge/actions/runs/35633633851/attempts/1)
  could not observe the Docker version within ten seconds, before any build.
  Its repeat reached the application and retained the expected readiness FAIL.
- The [source-topology attempt](https://github.com/noor15102002/cloud-forge/actions/runs/35633634065/attempts/1)
  retained one timed-out rollout probe, causing its qualification assertion to
  fail. The repeat completed all three topologies, preserving source-single
  availability FAIL and measured generated-single recovery FAIL. Its rollout,
  restoration and cleanup checks passed. The original reported one-millisecond
  downtime follows the [documented completion-to-completion convention](reporting.md);
  it does not include the timed-out request or establish total outage duration.

[CI on the actual merged revision](https://github.com/noor15102002/cloud-forge/actions/runs/35635658550)
passed. The [packaged Action on that revision](https://github.com/noor15102002/cloud-forge/actions/runs/35635658695)
is retained separately. Generic qualification alone does not establish that any
private application has run.

## Public import observation follow-up

The recurring import failure remains tracked in [#51](https://github.com/noor15102002/cloud-forge/issues/51).
The generic cancellation harness now records only the registered run's exact
bundled-fixture image A/B direct-import command. It reuses the existing bounded
stream recorder without retries, auxiliary runtime calls or deadline changes.
Foreign images, mismatched owners and unsupported command shapes are rejected
before execution or capture. Kubeconfig and application/provider logs are not
recorded.

The [diagnostic workflow](https://github.com/noor15102002/cloud-forge/actions/runs/35634489084)
qualified all four Redis jobs on `8a00151ebbde559a48218bc9597ec9cc23834a36`.
Both observed imports succeeded, with complete original streams and no recording
errors; actual and deliberately faulted cleanup cases retained their native
results and passed final cleanup. The earlier import exit 1 did not recur, so
its underlying cause remains unknown. Successful diagnostics do not supersede
the failed attempts.
