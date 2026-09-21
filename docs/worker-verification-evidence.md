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

The corrected fixture and verifier must pass a new disposable matrix before
protected merge and private worker pilots. Passing local race, safety, schema,
lint and helper checks is not a substitute for that runtime qualification.
