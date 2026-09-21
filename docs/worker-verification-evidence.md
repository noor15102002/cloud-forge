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

The worker branch includes the protected backend merge without changing its
already integrated code. The final main-based candidate and packaged Action
must qualify before protected merge and private worker pilots. Local checks or
generic fixtures alone are not evidence that any private application has run.
