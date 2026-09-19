# Evidence Reliability acceptance

Implementation: [PR #38](https://github.com/noor15102002/cloud-forge/pull/38).
Feature revision: `c2cb50b4b194805f1c902debcc0fd24da53ad3f3`.
The pull-request runtime jobs built GitHub merge candidate
`24f8ea1d748a8258ab6a6453b643e5b26e49aa8a`, recorded as the verifier identity.
Merged revision: `43d11049f7d4ea297ec1df1926981e28ec9ee2ad`.

See [the behavior and limits](evidence-reliability.md). Scope is limited to
runtime compatibility, topology-aware evidence, bounded restoration and
continuation, and planner explanations.

## Validation

- [Standard CI](https://github.com/noor15102002/cloud-forge/actions/runs/35443309167): formatting, race-enabled tests, vet, lint, vulnerability checking, Action validation and build.
- [Packaged Action](https://github.com/noor15102002/cloud-forge/actions/runs/35443309142): healthy and intentionally broken runtime cases.
- [Same-code topology](https://github.com/noor15102002/cloud-forge/actions/runs/35443309120): one/two source replicas and a generated single replica.
- [Reference/public-app/cancellation matrix](https://github.com/noor15102002/cloud-forge/actions/runs/35443309121): five trials per family, planted failures, two public apps and four interruption stages.
- [Redis and interruption](https://github.com/noor15102002/cloud-forge/actions/runs/35443309165): both fixture families, semantic failures, dependency deadlines and Redis cancellation.

All ten jobs passed their expected assertions after one Node job retry. A successful job can contain an expected application FAIL; reports keep that result. Runtime PASS can coexist with image-scan WARN.

All real runtime execution used disposable GitHub-hosted Ubuntu runners. The
local development machine cannot access its Docker daemon. No private
application source or runtime pilot was used in this milestone.

## Same application, different topology

| Topology | Replacement availability | Failed probes | Observed downtime | Restoration | Later rollout |
| --- | --- | --- | --- | --- | --- |
| source, 1 replica | FAIL | 29/147 | 6606 ms | PASS | PASS |
| source, 2 replicas | PASS | 0/418 | 0 ms | PASS | PASS |
| generated, 1 replica | PASS | 0/254 | 0 ms | PASS | PASS |

The source one- and two-replica cases have identical Node code, Dockerfile and
package metadata. Both use the same eight-second initial readiness delay;
only replica count changes. Every availability result labels its topology.
The generated case retains its actual observation and is not forced to fail
because it has one replica.

A failed replacement-availability requirement does not establish a defective
SIGTERM handler. Targeted in-flight shutdown is a separate experiment requiring
the explicit application control protocol. These samples do not establish a
universal zero-downtime guarantee.

## Original failure survives recovery

The final public Express run retained a rolling-deployment FAIL (2/171 failed
probes, 21 ms observed interruption), restored its baseline, and then completed
load with 41,642 requests and PASS. The public FastAPI run retained pod-recovery
and rolling-deployment FAIL results (27/149 and 8/189 failed probes, 183 ms and
60 ms observed interruption), restored after each mutation, and completed load
with 23,920 requests and PASS. Both overall reports remain FAIL. These are
observations of the selected public routes and source-derived test topology,
not universal application defect claims.

Regression tests also exercise unrecoverable baselines, wrong image/revision/
replicas, API errors, missing compatibility observations and cancellation.
BLOCKED and ERROR are not converted into application failures. HPA observation
errors preserve completed load measurements; interrupted load remains executed
ERROR. Failed prerequisites block unstarted load.

## Reference, dependency and cancellation coverage

The first five healthy Node trials and all five healthy Python trials passed
their required runtime assertions. Both Python planted failures were detected:
readiness-gating FAIL and targeted in-flight shutdown FAIL, each retaining a
successful restoration. The unchanged Node retry passed five further healthy trials and detected both planted shutdown/rollout failures.

Variance artifacts retain startup, recovery and rollout timings, P95 latency,
and throughput samples. Measurements are not silently pooled across attempts
or treated as calibrated regression thresholds.

Healthy Node and FastAPI Redis cases pass dependency startup, semantic readiness
and supported lifecycle/load checks. HTTP 200 with degraded semantic readiness
and disconnected applications retain FAIL. Dependency startup deadlines produce
BLOCKED application experiments rather than application startup failures.

Five interruption stages exercise real subprocesses: Docker build, k3d cluster
creation, application readiness, k6 execution, and Redis readiness. Each check
asserts bounded termination, no owned resource or private-workspace leak,
unchanged user kubeconfig, and unchanged unrelated container/network/volume
identities. Started readiness/load observations are executed ERROR; later
unscheduled experiments are SKIPPED. Earlier completed evidence survives.

## Compatibility and report preservation

The exercised bundle is Docker 28.0.4, k3d 5.9.0, kubectl 1.35.5,
Kubernetes 1.35.5+k3s1, k6 2.2.0 and Trivy 0.74.0. Runtime reports record all
six tools and the CloudForge version/commit. Known unsupported client/server
skew blocks execution; parseable untested versions remain `not_validated` and
cannot yield an overall PASS. Pure analysis and planning need none of these tools.

The strict loader accepted 39 saved v1alpha3 reports: 33 accepted matrix reports and six retained reports from the first Node attempt. Repeated rendering preserved observations. All three standalone topology plans validated against their v1alpha3 schema.

A previously saved v1alpha2 report retained all twelve experiment statuses and
measurements; topology was not synthesized. Earlier keep-alive observations
remain valid for their tested traffic. Current availability probes explicitly
use a new connection per probe. The initial topology harness was corrected after
it wrongly assumed a single replica must always produce a failed observation;
no prior measurements were rewritten.

The privileged reporter continues to check out trusted default-branch code.
Its pre-merge rejection of v1alpha3 is expected during this schema transition;
see the implementation pull request for post-merge reporter validation.
No untrusted pull-request code runs with the reporter's write permissions.

The [first Node job attempt](https://github.com/noor15102002/cloud-forge/actions/runs/35443309121/attempts/1) passed all five healthy trials, then stopped before
deploying the broken-shutdown fixture because the actual API version could not
be observed. CloudForge classified this as ERROR and cleaned up. That report is
retained; the unchanged retry passed the complete Node sequence and the API observation error did not recur. The original observation is not changed into PASS.

## Remaining limits

Restoration verifies runtime configuration and readiness; it does not reset
arbitrary business data. Five-trial variance is descriptive, not a calibrated
performance-regression threshold. Existing image-scan warnings remain visible
even when runtime checks pass. This acceptance does not certify production
readiness or arbitrary workloads.

Next comes a read-only private application audit, selecting one service and
proving its dependency requirements. PostgreSQL is deferred unless that audit
establishes a need. No new provider, workload category, dashboard, AI feature or
cloud backend was added.
