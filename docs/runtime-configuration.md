# Runtime configuration and pilot support

CloudForge tests one HTTP application with one selected Dockerfile and at most one
single-container Deployment. Explicit Redis provisioning and safe test bindings
are described in [dependency runtime](dependency-runtime.md). PostgreSQL, arbitrary
manifest application, Helm rendering and production credentials remain unsupported.
The historical stateless pilot evidence is separate from subsequent private pilots.

Place `cloudforge.yaml` at the application root or use `verify --config FILE`:

```yaml
schema_version: v1alpha1
runtime:
  port: 8000
endpoints:
  health: /health
  readiness: /ready
  load: /api/example
load:
  vus: 5
  duration: 20s
```

The file is limited to 64 KiB, must be regular, and rejects duplicate/unknown
fields. Endpoints must be application-relative paths without queries or external
hosts. Configuration overrides endpoint/port discovery; conflicting source
probe ports are rejected. Configuration never loads host environment values; v1alpha2 adds restricted explicit test bindings.
An omitted load endpoint skips load/HPA experiments. VUs are bounded to 1–32;
duration to 1s–1m. Defaults are five VUs and twenty seconds.

Supported source probe settings include HTTP/TCP/gRPC handlers, timing,
thresholds and termination grace. Exec probes are recognized as present, but
their command text is omitted and runtime verification rejects them. HTTP
headers/host overrides, lifecycle hooks, environment configuration, mounted
volumes, sidecars, scheduling and security overrides currently require a
supported test deployment instead of being silently copied or discarded.
Only CPU-utilization HPAs are supported; their scaling behavior is preserved.

CloudForge preserves replica count, resources, rollout strategy, minimum-ready
time and termination grace. Generated names, image references and Service
exposure are deliberately adapted to the isolated cluster. Missing resources
receive explicit defaults (100m/64Mi requests, 500m/256Mi limits), recorded in
the report. Replicas/HPA maxima above five are rejected. Aggregate workload
limits, including rollout surge, must fit four CPUs and 2 GiB. The cluster has
4 GiB and the private BuildKit builder has two CPUs/2 GiB. Build timeout is ten
minutes; readiness and lifecycle experiment windows are bounded to two minutes.
After importing the image, CloudForge separately waits up to 90 seconds for a
ready API and node without resource pressure before applying the workload.
These controls limit resource use; unfamiliar source still runs only on
disposable machines.

Kubeconfig and Docker builder configuration live in a private run directory.
The user's current Kubernetes context/default builder is not selected or
modified. Resources have unique run names and ownership metadata. Cleanup
attempts use independent deadlines after cancellation. No global Docker prune
or unrelated-resource deletion is performed. SIGKILL, daemon failure or host
failure may require manual removal of the report's named cluster and
`cloudforge-<run-id>` builder after confirming ownership. Base image caches may
remain; only run-owned images/build caches are deleted.

## Optional controlled experiment protocol

To opt into stronger behavioral proofs, an application in the disposable test
environment can implement this protocol and explicitly configure:

```yaml
experiments:
  control_path: /_test
```

The prefix is configurable. These endpoints are test instrumentation and should
not be exposed on a production service. CloudForge never guesses their presence.

| GET suffix | Response/action |
| --- | --- |
| `/identity`, `/state` | JSON `{"pod":"<hostname>","ready":true,"active":[]}` |
| `/unready` | Set application readiness false and return its identity/state |
| `/ready` | Restore application readiness and return its identity/state |
| `/slow?id=<generated-id>` | Register the ID in `active`, hold the request for five seconds, then return `{"pod":"<hostname>","completed":"<generated-id>","sigterm_received":true}` |

Readiness gating requires two replicas. CloudForge first observes Service
traffic to the selected pod, makes that pod unready through the private API
proxy, observes Kubernetes readiness, samples new Service connections, then
proves routing resumes after restoration. It does not count startup retries as
proof of readiness gating.

Targeted shutdown starts a request on a specific pod through the private API
proxy, observes that exact request ID in the pod's active set, requests ordinary
graceful deletion, observes termination and checks the response identity/ID.
The handler must record which request IDs were active when SIGTERM arrived;
`sigterm_received` confirms that the specific request overlapped the signal.
A Kubernetes deletion timestamp alone cannot establish this proof.
The existing Service-traffic shutdown experiment remains a separate measurement.
Applications without this protocol receive explicit skipped evidence for the
controlled experiments. Missing/incomplete control evidence is never a pass.

## Comparison and validation evidence

Reports record source commit/dirty state, image ID (registry digest when
available), CloudForge identity, runtime tool versions, platform/CPU count,
effective configuration/resources and a normalized workload hash. Baselines
require a complete matching compatibility key; source commits and image IDs
may differ. Dirty or unidentified CloudForge builds and missing image identity
do not establish a compatible baseline. Legacy reports remain readable but cannot establish compatible
runtime baselines. Timing tolerance remains 10%; repeated trials measure noise
and do not themselves prove statistical significance.

The pilot workflow runs five trials of each healthy Node/FastAPI fixture,
planted failures, four interruption stages and two pinned public apps. It
publishes full JSON reports and variance summaries. External tests use a
declared two-replica reference deployment; the small FastAPI example receives
a standard Dockerfile because upstream does not supply one. Application source
is not edited. Only each app's stateless GET `/` route is exercised. Legitimate
source findings, such as the Express image running as root, remain visible.
An external application is not assumed healthy: measured lifecycle traffic
failures are retained as application failures, with successful final health and
nonzero failed-request evidence. Successful baseline restoration allows later
independent experiments to run without removing those failures. The pilot checks that these are application observations, not CloudForge
execution errors; it does not turn the external application's verdict into PASS.

## Monorepo builds

Runtime configuration `v1alpha3` adds optional `build.app`, `build.dockerfile`
and `build.context`. All three paths are relative to the positional repository
boundary. See [single-workload selection](build-selection.md). Existing v1alpha1
and v1alpha2 configurations keep their original behavior.

## Explicit test topology

Runtime configuration `v1alpha4` adds bounded replica and rollout overrides.
They are labeled as explicit test configuration, preserving supported source
probes and per-pod resources. See [controlled topology comparison](controlled-topology.md)
for defaults, safety limits, HPA exclusions and interpretation of paired evidence.
