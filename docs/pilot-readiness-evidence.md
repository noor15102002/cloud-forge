# Pilot validation evidence — 2026-09-18

The implementation candidate `a01dd73` passed the disposable-runner pilot.
Peaxis and Avylo were not tested. These results establish the documented pilot
scope; they do not certify arbitrary applications as production-ready.

## Reproducible proof

- [Standard CI](https://github.com/noor15102002/cloud-forge/actions/runs/35402280425): formatting, race tests, vet, lint, govulncheck, Action validation and build.
- [Packaged Action runtime integration](https://github.com/noor15102002/cloud-forge/actions/runs/35402280423): healthy Node and deliberately broken shutdown/rollout behavior.
- [Full pilot and downloadable JSON artifacts](https://github.com/noor15102002/cloud-forge/actions/runs/35402280402): five trials per healthy reference family, planted failures, external apps and real interruption.

Each healthy trial passed build, deployment readiness, Service readiness gating,
targeted in-flight shutdown with explicit SIGTERM acknowledgment, pod recovery,
rollout and load. Scanner warnings remain visible: “healthy” describes expected
runtime behavior, not absence of vulnerabilities. Broken Node shutdown/rollout
and Python shutdown/readiness fixtures failed their intended experiments.

Four actual interruptions covered Docker build, k3d creation, application
readiness and k6 load. All removed run-owned resources while preserving an
existing kubeconfig/context and unrelated Docker container, network and volume.
The unrelated container also remained running. Cleanup checks ran after the
normal and intentionally failing fixture trials.

The pinned public Express app retained its root-user finding and observed
rollout request loss; the final endpoint recovered. The pinned FastAPI app
completed runtime experiments with scanner warnings. Their unmodified
application source ran with explicit two-replica test manifests; FastAPI also
received a standard Dockerfile because upstream provides none. Only stateless
GET `/` was tested. Neither app implements the optional control protocol, so
those stronger experiments were explicitly skipped. See the source revisions
and complete effective configuration in the JSON artifacts and
[`scripts/pilot-external.py`](../scripts/pilot-external.py).

## Five-trial measurements

Each trial used a fresh owned builder and k3d cluster. Fingerprints matched
within each family, including effective configuration/resources and tool
versions. The report records image identity and source/build revisions
separately from environment compatibility.

| Fixture | Measurement | Range | Mean | Sample standard deviation |
| --- | --- | ---: | ---: | ---: |
| Node | Startup (s) | 2.202–4.660 | 3.555 | 0.876 |
| Node | Recovery (s) | 2.979–4.957 | 3.974 | 0.699 |
| Node | Rollout (s) | 5.347–7.250 | 6.231 | 0.744 |
| Node | P95 latency (ms) | 131.573–151.777 | 138.462 | 7.780 |
| Node | Throughput (requests/s) | 199.098–206.993 | 203.499 | 2.988 |
| FastAPI | Startup (s) | 3.602–5.681 | 4.446 | 0.863 |
| FastAPI | Recovery (s) | 3.204–4.983 | 4.615 | 0.789 |
| FastAPI | Rollout (s) | 7.167–8.321 | 7.649 | 0.531 |
| FastAPI | P95 latency (ms) | 61.389–66.549 | 62.572 | 2.230 |
| FastAPI | Throughput (requests/s) | 191.457–197.597 | 193.203 | 2.498 |

Five samples expose natural variation but do not establish statistical
significance. The existing 10% timing heuristic can flag noise, particularly
for startup/recovery/rollout measurements. Metric-specific calibration is
tracked in [#31](https://github.com/noor15102002/cloud-forge/issues/31) and is
separate from status/error-count verdicts. Release packaging remains
[#11](https://github.com/noor15102002/cloud-forge/issues/11).

Local verification also passed race tests, vet, lint, govulncheck, build,
repeatable Node/Python analysis JSON, and the four-repository read-only
compatibility matrix. Local doctor correctly reported Docker daemon permission
denial; application execution for this milestone took place on disposable
GitHub-hosted runners.
