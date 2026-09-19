# Controlled topology comparison

An optional `v1alpha4` runtime configuration selects a bounded **test topology**:

```yaml
schema_version: v1alpha4
topology:
  replicas: 2
  rollout:
    strategy: rolling_update
    max_unavailable: 0
    max_surge: 1
```

Combine this with the existing build, endpoint and semantic readiness settings,
then pass the file with `verify --config FILE`. It can live outside the application
checkout. `analyze` and `verify --plan` still require no runtime tools.

`replicas` is required (1–5). The optional rollout block defaults to the values
shown above. Only `rolling_update` and integer counts are supported.
`max_unavailable` must be between zero and the replica count; `max_surge` between
zero and five. Both cannot be zero. The existing aggregate CPU/memory budget
includes rollout surge and dependency resources; unsafe plans are rejected
before building. No silent replica cap is applied.

Explicit topology replaces only source/default replicas and rollout strategy.
Source probes, timings, per-pod resources, minimum-ready time and termination
grace remain in effect. Unsupported source settings remain unsupported. A source
HPA blocks fixed topology configuration, since autoscaling would change the
controlled replica count. Existing configurations without topology retain their
behavior, including HPA support.

Plan and evidence provenance use `explicit_test_configuration` for topology,
replicas and strategy. A source reference may still identify the Deployment that
supplied probes and resources; field origins distinguish those from overrides.
Terminal/Markdown output spells out "Topology origin: explicit test configuration".
This is never a claim about the application's production configuration.

## Comparison protocol

Use the same source revision, Dockerfile, context, health contract, per-pod
resources, verifier and tool versions. Use explicit `max_unavailable: 0` and
`max_surge: 1` in both cases; change only replicas from one to two. Record image
identities and any build-cache differences. The extra replica increases total
resource consumption intentionally; per-pod limits and overall safety caps stay
fixed. Use isolated clusters and cleanup after each case.

For both cases retain startup/semantic readiness, graceful replacement, pod
recovery, rollout, failed probes, downtime, durations, sampled minimum Ready pod
counts, final health, baseline restoration and cleanup evidence. Ready counts
exclude terminating pods and are sampled during deletion as well as recovery;
short transitions between polls can be missed. Successful restoration must retain
the original FAIL. Service probes use fresh connections and do not prove targeted
in-flight request draining.

All measured outcomes remain valid, including PASS/PASS and FAIL/FAIL. A two-replica
improvement supports a topology effect; it does not certify production availability.
Persistent failures require investigation before assigning an application defect.
Different topology fingerprints intentionally cannot be graded as compatible
regression baselines; compare observations side by side.

## Contracts and qualification

Runtime configuration v1alpha4 and plan/report v1alpha5 add this capability. Report
v1alpha1–v1alpha4 and their published schemas remain readable and unchanged.
The generic `explicit-topology` CI job runs the unchanged shared-package monorepo
under both settings, checks deterministic planning/report loading, source integrity,
restoration, safety fingerprints and cleanup. It does not force availability PASS.

Private application comparison evidence is retained in the private pilot repository.
Backend dependency work is outside this milestone. The next decision follows a
read-only refinement of the selected backend's actual boot/readiness requirements.
