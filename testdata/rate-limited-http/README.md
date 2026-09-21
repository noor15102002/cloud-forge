# Rate-limited HTTP reference

This dependency-free Python service accepts at most 60 `/health` requests per
rolling minute per client address. Excess requests receive HTTP 429 with a
bounded JSON response. Accepted requests return `{"status":"ok"}`. The same
source, fixed four-second startup/draining delays, two-replica topology and
resource limits are used for every pacing case. Kubernetes probes use TCP, so
they do not consume the HTTP allowance. No application control or load endpoint
is provided.

`scripts/pilot-probe-pacing.py` compares omitted pacing with an explicit
two-second interval. A high-frequency run must retain its observed rate-limit
failure. A slower run must produce complete lifecycle observations and
restoration without 429; any other observed availability failure remains FAIL.
This fixture does not assert that two replicas or slower sampling guarantee
uninterrupted availability.
