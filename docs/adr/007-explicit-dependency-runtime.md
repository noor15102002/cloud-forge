# ADR 007: Explicit dependency-aware runtime

Status: Accepted for the Redis milestone.

CloudForge verifies narrowly supported workloads and must not infer that a
client library authorizes connecting to an existing database. A controlled
private-service audit established a need for isolated Redis and semantic
readiness, without changing the application or using production credentials.

Extend the existing planner, official Kubernetes types, runner and cleanup
lifecycle. A first-party Redis resource provider proves the abstraction; no
empty database interfaces, provider plugin framework or arbitrary Compose runner
are introduced. Dependencies are explicit, bounded, internal-only and owned by
the disposable cluster. Their resources count against aggregate capacity.

Allow named generated endpoint bindings and restricted explicit test literals.
Do not import environment files or Secrets. Omit literal values from reports and
include their configuration hash in compatibility decisions. Redis needs no
credential capability while it is unauthenticated/test-only inside an isolated
cluster; authentication may be a separate decision later.

Plan capabilities before execution. Required unsupported/unresolved capabilities
are BLOCKED; optional absent experiments are SKIPPED. Keep dependency startup
failure, application behavior and execution errors distinct. HTTP status and
optional flat JSON assertions establish different readiness evidence; assertions
do not silently change Kubernetes routing behavior.

Publish v1alpha2 reports/config extensions and preserve v1alpha1 loading. Include
dependency image/configuration identity in baseline compatibility. Validate with
generic reference services, planted failures, real startup interruption and
ownership checks before a separate private-service pilot.

Dependency-loss experiments, authenticated POST profiles, PostgreSQL, dynamic
provider registration and general multi-service orchestration are deferred.
