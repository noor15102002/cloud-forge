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
- Existing generic backend and worker cases, retained as experimental evidence.
- The packaged root Action using the same archive for Node and monorepo runs.

Focused Go regressions additionally cover malformed/truncated cleanup inventories,
remaining resources, ownership collision, idempotency, temporary-directory failure,
restoration severity, scan normalization and historical schema compatibility.
These injected tests establish specific fault semantics; they are not substitutes
for the exact-binary disposable runtime cases.

## Preserve failed attempts

Each artifact name includes the workflow run ID and attempt number. Keep the
original reports, stderr, exit records, command-observer records, cleanup results,
manifest and checksums, including failures. Native public fixture capture writes
streams before assertions and refuses to overwrite earlier evidence. A later
retry is a new attempt; never replace an application FAIL with a preferred run.
Do not automatically retry a fixture to make a release gate green.

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
