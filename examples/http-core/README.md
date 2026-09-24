# HTTP-core first run

This dependency-free Node HTTP service demonstrates the bounded HTTP core.
Use the [first-run guide](../../docs/first-run.md) to install CloudForge and its
runtime prerequisites, then run from the CloudForge repository root:

```sh
cloudforge analyze examples/http-core
cloudforge verify examples/http-core --plan
cloudforge verify examples/http-core --format json > verification.json
cloudforge report verification.json --format text
```

The source Deployment declares two replicas, readiness/liveness probes, bounded
CPU/memory, a 15-second termination grace period and a RollingUpdate strategy
with zero unavailable replicas and one surge replica. These are **example source
settings**, not inferred production settings for another application. The server
marks itself unready on SIGTERM, permits a short routing propagation interval,
then closes and drains its HTTP server.

`/health` proves that the process answers HTTP; `/ready` reports its readiness;
`/work` serves a small JSON response under five virtual users for twenty seconds.
This is an onboarding workload, not a business transaction or performance
benchmark. It has no Redis, HPA, worker, preparation step, or experiment control
protocol. HPA, controlled readiness gating and targeted in-flight shutdown should
be explicitly SKIPPED. Ordinary sampled replacement and rollout observations
remain measured outcomes; two replicas do not force them to PASS.

Keep the checkout unchanged until verification finishes: both lifecycle images
are built from the live checkout. Do not add production credentials. CloudForge
adapts these manifests to uniquely named disposable resources and attempts owned
cleanup on completion or cancellation. See [security](../../docs/security.md)
for the execution boundary and retained-resource recovery.

The `node:22-alpine` base tag is resolved at build time. The report records the
built image identity and scanner findings; the example does not promise identical
container bytes across dates. CloudForge release archive reproducibility is a
separate guarantee.
