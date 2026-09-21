# Bounded HTTP probe pacing

Rate-limited health endpoints can reject CloudForge's availability probes with
HTTP 429. That is a real response under the tested request policy. CloudForge
preserves it as evidence; it neither ignores 429 nor changes application limits.

Runtime configuration `v1alpha7` accepts an explicit minimum interval between
CloudForge HTTP probe starts:

```yaml
schema_version: v1alpha7
runtime:
  port: 8000
endpoints:
  health: /health
  readiness: /health
probes:
  interval: 2s
```

The interval is bounded from `20ms` through `5s`, in whole milliseconds. It is
normalized in plans and reports. The setting is HTTP-only; worker contracts
reject it. Omitting `probes` keeps existing behavior, including the availability
sampler's 20 ms interval. CloudForge does not choose a slower policy automatically.

One pacing boundary is shared across CloudForge readiness and availability
requests in a verification run, including baseline validation and lifecycle
observations. It controls the earliest next request start, not request timeout,
experiment deadline, or a promised number of samples. Slow requests can further
reduce the sampling frequency. Cancellation interrupts pacing waits.

Kubernetes startup/readiness/liveness probes retain their source configuration.
Explicit experiment control requests and k6 workloads keep their separate
contracts. Account for these requests when choosing a policy for a rate-limited
service. The setting does not add retries, change accepted HTTP statuses, or
extend experiment/cleanup bounds.

Plans label the policy as explicit test configuration. The effective setting is
also included in the runtime fingerprint, so different policies cannot silently
share a compatible regression baseline. A paired run with changed pacing is a
comparison of measured observations under different test conditions.

Slower sampling can miss outages shorter than the interval. A PASS means the
configured observations met their requirement; it does not prove uninterrupted
availability between requests. Existing downtime counters retain their documented
completion-to-completion convention. Preserve the original run when changing
pacing, including any 429 failures, rather than replacing that evidence.

## Generic qualification

The `probe-pacing` reliability job uses the same dependency-free Python service
and pinned container base in both cases. Its `/health` endpoint accepts at most
60 requests per minute per client, and its Kubernetes probes use TCP. Only
`probes.interval` changes between the omitted-policy and two-second cases.

The gate requires a real observed 429 failure with default pacing, followed by
complete lifecycle observations and restoration with the explicit policy. It
does not require every availability verdict to become PASS. It retains source
hashes, image filesystem layer hashes, policy fingerprints, native output, exit
codes, and cleanup evidence. A separate existing public readiness-cancellation
case runs with the same explicit two-second policy and checks ownership cleanup
and unchanged user state. Unit tests separately interrupt a pacing wait before
another request or mutation can start.
