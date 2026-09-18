# Pilot readiness

This milestone establishes trustworthy verdicts and bounded, representative
tests before testing Peaxis or Avylo. Neither project is part of this milestone.
Reference fixtures and explicitly selected public stateless applications run
on disposable GitHub-hosted runners.

Implementation: [PR #29](https://github.com/noor15102002/cloud-forge/pull/29).
Tracking: [issue #30](https://github.com/noor15102002/cloud-forge/issues/30),
[public milestone](https://github.com/noor15102002/cloud-forge/milestone/6).

The acceptance gates passed on candidate `a01dd73`. See the
[dated evidence record](pilot-readiness-evidence.md) for CI, complete artifacts,
measured variance and scope limits.

## Acceptance gates

- [x] Correct root-user, probe, HPA, ORM, unresolved-port and nonregular-file verdicts.
- [x] Preserve supported deployment semantics and reject unsupported settings before execution.
- [x] Isolate kubeconfig and enforce replicas, CPU/memory and execution budgets.
- [x] Support strict versioned endpoint/load configuration without fixture-specific query parameters.
- [x] Record effective configuration, source/image identity and tool fingerprints before comparisons.
- [x] Prove controlled readiness removal and a targeted in-flight shutdown request, or explicitly skip when unsupported.
- [x] Provide runnable healthy/broken Node and FastAPI reference applications.
- [x] Run each healthy reference application five times and publish variance.
- [x] Exercise real interruption during build, cluster creation, readiness and load, with ownership/cleanup checks.
- [x] Verify two or three pinned external stateless applications without application-specific core behavior.

Resource settings above the safety budget are rejected, not silently reduced.
Baselines may compare different source commits/images, but require compatible
experiment and environment fingerprints. Missing evidence is not a pass.
SIGKILL/host failure require a documented recovery path; Docker/k3d is not a
security boundary for arbitrary code on a developer workstation.

Release packaging remains a separate follow-up after these gates pass.
