# Prerelease identity and qualification

The first public candidate is `v0.1.0-alpha.1`, Linux/amd64 only. Publication is
conditional on the complete qualification record. The presence of a build or a
historical green workflow is not release approval.

`make release RELEASE_VERSION=v0.1.0-alpha.1 RELEASE_OUTPUT=/tmp/candidate`
requires a clean committed checkout and Go 1.27.1. It injects the version, full
40-character HEAD commit and that commit's UTC timestamp. The timestamp is the
reproducible build date (`SOURCE_DATE_EPOCH` convention), not a claim about the
wall-clock time of every rebuild. The build uses fixed Linux/amd64 settings,
disabled cgo, trimmed paths and deterministic archive metadata.

The output contains `cloudforge_v0.1.0-alpha.1_linux_amd64.tar.gz`, `checksums.txt`
and `release.json`. The archive contains the binary and license. The manifest
records version, full commit, date, target, compiler version, source epoch,
archive SHA-256 and binary SHA-256. `cloudforge version --format json` reports the
same version, commit and date. Runtime reports retain that version and commit in
`producer`; old reports are not given new producer metadata.

Current manifests use `manifest_schema_version: 2`. They also record supported
config/report schemas, the expected pinned runtime tool bundle, and actual build
execution times as `qualification_build_started_at` and
`qualification_build_finished_at`. Docker/buildx are explicitly runner-provided;
their observed versions remain in the runtime evidence. These wall-clock fields
do not enter the binary or archive. Reproduction uses separate compiler caches
and compares both actual binary/archive hashes and every deterministic manifest
field, excluding only the two validated execution timestamps. The installer can
read the earlier ten-field manifest, but current candidate qualification requires
the enriched manifest; historical manifests do not acquire invented metadata.

## Candidate gate

Use the normal protected-main PR workflow. After the final merged revision is
known, dispatch **Release candidate qualification** on that exact revision. A
squash or merge that changes the commit requires a new candidate and complete
qualification; a feature-branch binary must not be relabeled as the merged one.
Do not add source changes after qualifying and claim the previous result applies.

The workflow builds an archive once, independently reproduces it byte-for-byte,
and shares the original artifact with every runtime job. Installation validates
manifest shape, checksum, safe archive members and the executed binary's complete
identity. No qualification job substitutes a newly built development binary.
The installation receipt records its actual UTC `installed_at` separately from
the reproducible embedded date and the build execution timestamps.

Development qualification and archive qualification are separate decisions.
Passing source-level checks or a synthetic PR merge binary permits consideration
of a protected merge; only the installed canonical archive can receive release GO.
The dispatch requires the replacement PR number explicitly for scoped reporting;
the historical closed PR is not a hardcoded destination.

The required archive matrix covers the declared HTTP/Redis core. Public reference
copies use an explicit `stable-http-core` profile: HPA manifests and optional
control endpoints are omitted before analysis, while application/build bytes,
replica settings, resources and load bounds are unchanged. Source/effective file
hashes and exclusions are retained with each attempt. Experimental experiments
must be SKIPPED and unexecuted in this profile. This is a declared test scope,
not a reinterpretation of older HPA, worker or controlled-test results.

The original full reference, backend and worker suites remain available through
explicit development workflow dispatches. Their outcomes remain experimental;
they are not installed-archive qualification claims. An experimental failure
alone does not block HTTP-core release, but a shared execution, dependency,
identity, observation, artifact or cleanup defect does. Automatic PR checks keep
the protected `test` and `readiness` names; broad runtime suites do not restart
on every PR edit.

Qualification keeps three outcomes separate: each native CloudForge invocation,
the harness assertion about its expected behavior, and cleanup observations.
A deliberately broken fixture can produce native FAIL/exit 1 while its
qualification assertion passes. An injected observation failure can correctly
produce native ERROR/exit 2. Neither outcome establishes cleanup without its own
retained evidence. Missing native output or required cleanup evidence fails the
current qualification; it is never interpreted as success.

The gate includes:

- All Go tests, race tests, vet, lint, vulnerability checks and reporter/Action tests.
- A clean hosted Linux/amd64 installation, exact version identity and `doctor`.
- Healthy Node/FastAPI, Redis, selected monorepo, topology, sampled pacing and
  public external fixtures, including intentional application failures.
- A copied public Node service with readiness held false: actual 0/2 Ready,
  observed healthy node conditions, startup FAIL/exit 1 and cleanup PASS. The
  original source hashes and exact fixture changes are retained; root cause stays
  unestablished in the verifier's retained diagnostic.
- Exact-binary operational fault injections: invalid Trivy envelopes, case aliases, subject
  mismatch, scanner exit failure, Docker unavailable, storage/registry build
  infrastructure failure and unavailable node observation.
- An incomplete exit-0 cleanup inventory after an observed rollout FAIL, requiring
  cleanup ERROR while retaining the original application failure.
- Build, cluster, Redis, readiness, lifecycle mutation and load cancellation;
  unrelated resource and kubeconfig sentinels; independent cleanup checks.
- Separately retained experimental backend, worker, HPA and controlled-test
  evidence, outside the archive's qualified HTTP-core contract.
- The packaged root Action using the same archive for Node and monorepo runs.

Focused Go regressions additionally cover malformed/truncated cleanup inventories,
remaining resources, ownership collision, idempotency, temporary-directory failure,
restoration severity, scan normalization and historical schema compatibility.
These injected tests establish specific fault semantics; they are not substitutes
for the exact-binary disposable runtime cases.

Each acceptance record must identify its proof type:

| Proof | What it establishes |
| --- | --- |
| A — Installed-archive runtime | The canonical archive was verified, installed and executed against the recorded runtime or declared fault wrapper. |
| B — Source-level regression | Source tests or test doubles establish a specific parser, status, ownership, rendering or restoration contract. They do not execute the distributed archive. |
| C — Live platform/integration | Actual GitHub Action, comment, artifact and disposable runner operations, with retained identity and outcomes. A case may also have installed-archive proof. |
| D — Historical private pilot | An earlier private application observation. It is not current candidate qualification and must not be republished as generic public evidence. |

The public CLI has no command that invokes the same completed Service object's
cleanup twice. Direct repeated-adapter cleanup therefore remains source-level
proof; installed-archive fault cases separately exercise already-absent resources
and fallback removal. Do not describe those as the same test. A fault harness may
remove its own deliberately retained resources after observing the verifier's
cleanup ERROR, but that later harness teardown must never be reported as
CloudForge cleanup PASS.

## Preserve failed attempts

Each artifact name includes the workflow run ID and attempt number. Keep the
original reports, stderr, exit records, command-observer records, cleanup results,
manifest and checksums, including failures. Native public fixture capture writes
streams before assertions and refuses to overwrite earlier evidence. A later
retry is a new attempt; never replace an application FAIL with a preferred run.
Do not automatically retry a fixture to make a release gate green.

Native streams and bounded invocation records are captured before assertions.
Independent cleanup and its structured observation run before bulk upload. A
compact qualification record is surfaced separately, and small and bulk uploads
have independent deadlines. Bulk preservation remains a required gate even when
the compact record survives; its failure does not erase the native result or
cleanup observation. The independent observer never deletes resources, checks
builders and owned dangling images as well as container/network/volume/image
inventories, and compares the recorded kubeconfig and direct temporary-workspace
baseline. Nested temporary roots require their individual harness checks.

Always/finally paths only help while the runner can execute them. Runner loss,
hard termination or a platform outage can prevent cleanup and evidence upload.
If observations cannot be recovered, retain UNKNOWN rather than inferring cleanup
from VM termination or from an empty replacement runner. A historical UNKNOWN
does not have to become PASS before a new candidate can qualify. Preserve the
original attempt, fix any demonstrated harness weakness, document the reason and
corrected revision, and allow one controlled fresh qualification. A demonstrated
product defect must be fixed first. Do not rerun an old workflow revision and
claim that it tested a newly corrected harness, and do not repeat an application
failure merely to obtain a preferred result.

Artifacts retain 90 days in Actions. Before publication, download and retain all
qualification attempts with the engineering report and list every run URL and
result. Release notes must explicitly retain failed attempts, the reason for any
subsequent change/retry, and the final qualification run. Attach the durable
qualification record to the prerelease so expiring CI retention is not the only
public record.

## Publication

Tag the exact qualified commit and upload the already-qualified archive,
`checksums.txt` and `release.json` without rebuilding or replacing bytes. Verify
checksums again after download from the draft/prerelease. Publish only a prerelease,
with the exact commit, scope, limitations, clean-install result and qualification
record. Other targets are not qualified or distributed by this milestone.

The external root Action must be pinned to that full commit. It downloads the
selected release assets and verifies that their producer commit matches the
Action pin. Local `uses: ./` remains a development convenience; candidate
qualification supplies the explicit checked artifact inputs. Source builds or
moving Action tags are not the released binary path.
