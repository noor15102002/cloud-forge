# Changelog

All notable changes will be documented here. CloudForge follows Semantic
Versioning once releases begin.

## Unreleased — narrow prerelease hardening

- Stage image imports through the owned k3d image volume, preserve importer
  failures and require an exact Docker-to-CRI image identity match before use.
- Continue independent Redis qualification cases only after confirmed cleanup;
  retain early execution errors and fail the complete qualification contract.
- Isolate Trivy configuration, ignore rules, environment and cache; record the
  explicit vulnerability policy without allowing ambient settings to hide findings.
- Resolve Docker selection once, reject unsupported endpoints before execution,
  and pin the approved local socket across preflight, builds, scans and cleanup.
- Contain runtime-tool temporary files inside the owned workspace so normal
  cleanup also removes k3d hosts-file remnants observed during qualification.
- Align doctor with runtime compatibility and private-config Buildx availability;
  record the observed Buildx version without claiming an unvalidated version is supported.
- Label core/experimental capability maturity independently from native verdicts,
  and add a core-only first-run example plus complete consumer Action instructions.
- Separate bounded HTTP-core qualification from experimental HPA/worker results;
  retain explicit fixture profiles and original failures without regrading them.
- Retain allowlisted public-fixture Docker prerequisite and k3d import diagnostics
  without retries, deadline changes or application log capture.
- Reject unusable, ambiguous or mismatched Trivy observations as ERROR; retain
  collision-safe normalized package findings and concise image-A scan summaries.
- Establish invocation ownership before cleanup and verify complete removal,
  including private workspaces; preserve original application failures.
- Separate environment and observation errors from failed application requirements;
  retain safe startup/final HTTP reasons and precise sampled availability wording.
- Correct bounded controlled-readiness, targeted-recovery and HPA headroom verdicts;
  retain these capabilities as experimental for the prerelease.
- Preserve all eight historical report schemas, reject duplicate keys, improve
  Markdown/comments and treat numerical performance comparisons as advisory.
- Prepare reproducible, stamped Linux/amd64 archives and an exact-artifact
  qualification workflow. Publication remains conditional on candidate evidence.

Earlier entries below describe historical implementation milestones. The current
release promise is defined by [the support and maturity table](docs/supported-applications.md),
not by historical milestone wording.

## Unreleased — bounded HTTP observation

- Add explicit 20ms–5s HTTP probe pacing shared across readiness and lifecycle
  observations, with plan explanations and incompatible-baseline detection.
- Preserve measured HTTP failures, bounded cancellation, and historical reports;
  input v1alpha7 and report/plan v1alpha8 describe the new optional policy.
- Record sanitized current/previous application-container termination snapshots
  for HTTP readiness and worker observations without application logs or messages.
- Add a generic rate-limited HTTP comparison and cancellation qualification gate.

## Unreleased — isolated backend contract

- Add fixed PostgreSQL/pgvector and ClamAV providers, generated test configuration,
  bounded preparation, explicit backend capacity, and restricted runtime networking.
- Preserve exact preparation failures and unobserved states; retain raw-output
  omission and historical report loading with report v1alpha6/input v1alpha5.
- Build both rollout images before allocating the larger backend cluster.
- Add generic backend runtime qualification with failure and network controls.

## Unreleased

- Confirm incomplete lifecycle state with one bounded final observation when the requirement deadline interrupts a Kubernetes read; retain ERROR for unobservable or indeterminate completion and preserve earlier reports.
- Require a fresh healthy lifecycle prerequisite, revalidate the original baseline after rollout image preparation, and keep Kubernetes observation deadline errors distinct from failed application requirements.
- Add bounded sanitized HTTP status/failure counters and explicit sampling/censored-window metadata without changing historical reports or the verification schema.

- Mark semantic readiness BLOCKED when test infrastructure prevents any HTTP response; preserve actual response verdicts, attempted measurements and historical reports.

- Add bounded explicit test topology with replica/rollout provenance, resource-budget checks and fixed-topology/HPA exclusion.
- Sample Ready pod counts during deletion and recovery; retain original failures and restored baselines in paired topology evidence.
- Add runtime configuration v1alpha4 and plan/report v1alpha5; preserve historical report loading and update the trusted Action reporter.

- Add single-workload monorepo selection: independent app root, custom Dockerfile and build context with repository-relative provenance and safe path validation.
- Apply the same selection to images A/B, capability plans and environment fingerprints; add `analyze --config` and Action `config-path`.
- Add v1alpha3 configuration, v1alpha2 selected analysis and v1alpha4 plan/report contracts while retaining historical report loading.
- Add a runnable shared-package workspace fixture and disposable build, lifecycle, wrong-context and cancellation acceptance checks.

- Add v1alpha3 evidence reliability: checked tool/server compatibility and verifier identity, effective test topology, preserved failures with bounded baseline restoration and continuation, and explicit plan versus execution reporting.
- Align kubectl 1.35.5 with the explicitly selected k3s 1.35.5 runtime; fingerprint both client and server.
- Keep v1alpha1/v1alpha2 reports readable and runtime configuration unchanged.


- Added explicit Redis dependency provisioning with a digest-pinned internal-only image, shared resource budgeting and dependency startup evidence.
- Added restricted test environment bindings, optional bounded flat JSON readiness assertions and a read-only `verify --plan` capability report.
- Added v1alpha2 reports/config extensions, BLOCKED outcomes, dependency fingerprints and legacy v1alpha1 report/config compatibility.
- Added generic Node/FastAPI Redis fixtures plus dependency timeout/cancellation, cleanup and semantic-readiness validation paths.
- Confirm imported images in the isolated node before deployment; unavailable test images produce execution errors instead of application startup failures.

- Pilot implementation: correct root/probe/ORM/port analysis, bounded regular-file reading, faithful supported workload settings, private runtime configuration and resource budgets.
- Explicit endpoint/load configuration, compatible environment fingerprints and optional controlled readiness/in-flight shutdown experiments.
- Runnable Node/FastAPI reference fixtures with five passing healthy trials each, detected planted failures, real interruption/ownership tests and two pinned public applications. See docs/pilot-readiness-evidence.md for measurements and limitations.

- Added graceful shutdown and synthetic version A-to-B rolling deployment experiments under continuous HTTP traffic.
- Added bounded k6 load profiles with normalized throughput, error-rate, and latency evidence.
- Added CPU-based HPA scale-up observation with explicit missing-metrics skip diagnostics.
- Added canonical verification JSON, a versioned JSON Schema, concise terminal summaries, and pull-request-ready Markdown reports.
- Added strict explicit baseline files with deterministic status and normalized measurement comparison.
- Added separate regression, improvement, and unavailable sections to JSON, terminal, and Markdown reports.
- Added a composite Ubuntu verification action with pinned runtime tools and JSON/Markdown artifacts.
- Added a trust-separated workflow that validates untrusted report JSON before updating one bot-owned pull-request comment.
- Added `cloudforge report` for safe rendering of saved verification JSON without repository execution.
- Hardened verification preflight and cleanup so analyzer diagnostics survive, interrupted builds remain execution errors, partial clusters are removed, and cleanup failures do not starve later removals.
- Hardened the public Action with canonical workspace paths, strict artifact and run input validation, Node 24 action runtimes, isolated temporary directories, and installation-token-compatible PR comment ownership.
- Added a reproducible read-only compatibility matrix for pinned external Node.js, TypeScript, and Python repositories.

### Added

- Initial Go CLI with version, environment doctor, and repository analysis.
- Deterministic Node.js, TypeScript, Python, Dockerfile, and Kubernetes metadata analysis.
- Versioned JSON contracts, fixtures, documentation, and CI foundation.
- Docker-to-k3d verification with container-build and Deployment-readiness evidence.
- Cleanup guarantees for successful, failed, timed out, and canceled verification runs.
- Deterministic container and Kubernetes configuration findings with remediation and provenance.
- Bounded Trivy image scanning with normalized vulnerability findings.
- HTTP startup/readiness measurements and controlled pod recovery under continuous traffic.
