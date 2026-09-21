# Evidence Reliability Milestone

This milestone improves existing verification evidence. It adds no workload
category, dependency provider, cloud backend or application remediation.

## Compatibility and build identity

The installer selects kubectl 1.35.5 and k3d 5.9.0. Cluster creation explicitly
selects `rancher/k3s:v1.35.5-k3s1`; it does not inherit a different local k3d
default. CloudForge checks the client against the planned server before builds,
and checks the actual private cluster's client/server pair before application
import or deployment. Both versions enter the fingerprint alongside Docker,
k3d, k6 and Trivy.

- `supported`: the tested tool version, or a client/server pair within the
  Kubernetes same-major, one-minor kubectl skew policy.
- `unsupported`: a known violation of that skew policy; verification is BLOCKED
  (exit 1), with cleanup if the private cluster already exists.
- `not_validated`: a parseable version outside the tested bundle. Measurements
  may proceed, but the report exposes the limitation and cannot be an overall
  PASS. This is distinct from known incompatibility.
- Missing, malformed, truncated or failed version observations are ERROR (exit
  2). They never become application failures.

The tested bundle is Docker 28.0.4, k3d 5.9.0, kubectl 1.35.5,
Kubernetes 1.35.5+k3s1, k6 2.2.0 and Trivy 0.74.0. A supported tuple is not
proof that every possible command is bug-free.

Runtime reports require a CloudForge version and full source commit. Local Go
VCS metadata or explicit linker flags supply the identity; `dev` is an honest
unreleased version. A dirty VCS build retains `+dirty` and is not eligible for
baseline comparison. Unknown identity blocks execution as ERROR. Analysis and
`verify --plan` remain read-only and require neither tools nor build identity.

Previously collected HTTP observations are not rewritten because their tool
tuple was outside today's policy. Reports remain immutable evidence of those
runs. Their measurement scope and compatibility limitations still apply.

## Availability topology

Every availability experiment records the intended test replica count,
source-derived or generated topology, replica/strategy origin, rollout
strategy, maxUnavailable/maxSurge, termination grace period, and effective
probe timings/thresholds. Omitted Kubernetes values are shown as Kubernetes
defaults; Recreate has null maxUnavailable/maxSurge. The HTTP readiness route
and optional semantic contract are separate from the Kubernetes probe.
`readiness_origin` identifies the Kubernetes probe origin; an explicit HTTP
experiment endpoint can differ from that source probe.

A single-replica replacement can fail the tested zero-interruption requirement
while the application correctly handles SIGTERM. Service traffic alone cannot
establish whether a particular in-flight request survived termination. Only
the explicit targeted control experiment can establish that narrower claim.
Generated test topology is not presented as a production deployment defect.
Service availability probes use a new connection for each request, recorded in
`topology.availability_probe_connection_policy`. Earlier keep-alive observations remain valid for
the traffic they measured; neither policy proves arbitrary client workloads.
HPA evidence records the fixed starting topology and separately measures actual
replica changes; no PodDisruptionBudget is inferred or provisioned.

## Restore and continue

The existing sequential experiments share an intended runtime baseline. There
is no general workflow engine or concurrent experiment scheduling.

Before each applicable experiment, CloudForge checks:

1. The selected original image and requested replica count in the Deployment.
2. The controller's observed generation and ReplicaSet revision, with pods
   owned by that revision and using the original image.
3. Exactly the requested Ready pods, with no terminating or surplus pods.
4. A Ready pinned Redis pod when declared; its readiness probe checks PING.
5. Successful HTTP through the Service and the configured semantic readiness
   contract, when present.

A mutation attempt is recorded even if a command returns an error after
possibly changing the cluster. After an attempted mutation, CloudForge reapplies
the original workload manifest, restores controlled readiness when relevant,
and removes the run-owned HPA when necessary. It validates the baseline again
within a separate two-minute restoration budget. Image B is rolled back to
image A before subsequent experiments. Restoration does not reset arbitrary
business data or claim that application internals are identical.

- A valid failed requirement remains FAIL even after successful restoration.
- Successful restoration permits later independent experiments.
- An unhealthy baseline produces BLOCKED dependent experiments. A restoration
  API/observation failure produces ERROR and blocks dependents.
- An experiment execution ERROR remains ERROR even if its baseline recovers
  and later experiments pass.
- Cancellation stops scheduling immediately, preserves collected evidence,
  and attempts bounded cleanup using independent contexts. It does not start
  another experiment or restoration cycle.
- Started but interrupted readiness/load observations are ERROR with execution
  recorded, not unexecuted SKIPPED entries. An HPA observation error retains an
  already completed load result and its measurements.

An overall report containing an original FAIL cannot become PASS after
restoration. ERROR takes precedence for the process exit status, while the
original failed observation remains in its own evidence entry.

## Plan versus evidence

`verify --plan` explains detected metadata, supported capabilities,
prerequisites, topology origin, expected mutations, recovery strategies,
limitations and skip reasons. Runtime compatibility is not asserted by this
pure inspection. Runtime reports separately contain execution flags, observed
statuses, measurements and recovery checks. Planned support is never a PASS.

The status vocabulary is unchanged: FAIL means a valid observation failed a
tested requirement; BLOCKED means a required prerequisite was not established;
SKIPPED means inapplicable, unsupported or intentionally excluded; ERROR means
CloudForge could not execute or observe reliably.

Verification and standalone plan schemas are `v1alpha3`. Runtime configuration
remains `v1alpha1`/`v1alpha2`; saved verification reports from both versions
remain readable without synthesizing missing topology or compatibility fields.
Cross-schema baselines are not compared.

## Acceptance evidence

Local regression tests cover early failure followed by successful restoration
and continuation, failed restoration, image/revision/replica mismatches,
observation errors, cancellation, compatibility policy, and report loading.

The disposable `Evidence reliability` workflow runs the same healthy Node
application code under one and two source-derived replicas, plus a generated
single-replica topology. Application/Dockerfile hashes must match across cases. Both source-derived
cases use the same eight-second initial readiness delay, creating a controlled
replacement gap after the old process exits; replicas are the only difference.
The generated case records its actual outcome without forcing a failure solely
because it has one replica.
Observed single-replica availability failures must remain in the report while later
pod-recovery and rollout evidence are collected. Two replicas must maintain
availability in the exercised case. Both topologies use the same application
behavior; these results do not establish universal zero-downtime guarantees.

The final reference, Redis, public stateless application, packaged Action and
real cancellation/cleanup matrix passed. See the [validated acceptance record](evidence-reliability-evidence.md)
for run links, measured topology differences, the retained API-observation error
and successful unchanged retry, and the remaining limits.

## Frozen follow-up order

After this milestone: a read-only private application audit, select one service, prove its
actual dependency requirements, PostgreSQL only if needed, an explicitly
scoped private pilot, bounded richer HTTP workloads, then prerelease packaging.
No private application runtime test is part of this milestone.

## Lifecycle deadlines and final observation

A lifecycle requirement deadline is distinct from a failed Kubernetes read.
If expiry interrupts the last pod observation, CloudForge makes one fresh read
with a shared five-second final observation/HTTP health budget. A valid snapshot
that still shows an incomplete replacement or rollout establishes FAIL. If that
read fails or cannot be decoded, the result stays ERROR. A healthy snapshot only
after expiry cannot establish when the operation completed and also stays ERROR,
unless recorded traffic failures already establish a failed requirement.
Reports mark successful final snapshots as observed after the requirement
deadline. The extra read does not extend the time allowed to pass, generate more
traffic, or change the workload. Cancellation interrupts both observation
contexts. Previously saved results keep their original statuses and evidence.
