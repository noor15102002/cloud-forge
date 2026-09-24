# GitHub Action

The released composite Action supports GitHub-hosted Linux/amd64 runners. Pin it
to the full commit recorded in the prerelease's `release.json`. It installs the
checksum-verified release archive, checks version/full commit/build date, installs
pinned runtime tools, verifies one application and uploads JSON and Markdown.
It does not rebuild CloudForge during a released invocation.

```yaml
name: CloudForge
on:
  pull_request:
permissions:
  contents: read
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: noor15102002/cloud-forge@FULL_40_CHARACTER_RELEASE_COMMIT
        with:
          release-version: v0.1.0-alpha.1
          path: .
          artifact-name: cloudforge-verification
```

Replace the placeholder with the exact released commit. A missing archive,
checksum mismatch or mismatch between Action pin and binary producer stops
execution. The Linux/amd64 runtime tool set is k3d 5.9.0, kubectl 1.35.5,
Trivy 0.74.0 and k6 2.2.0; downloads are checked against upstream checksums.
Docker is supplied by the disposable hosted runner.

Local `uses: ./` builds a development binary when no candidate is supplied.
The release qualification workflow supplies `candidate-path`, `candidate-version`,
`candidate-commit`, `candidate-date` and `candidate-sha256` together. These internal
qualification inputs install the already-built candidate and verify it matches
the checked-out Action source. They are not an invitation to trust artifacts
chosen by pull-request code in a privileged workflow.

## Inputs and evidence

`path` is workspace-relative and must resolve within the GitHub workspace.
`config-path` selects an existing workspace-contained configuration file; build
paths inside the file are relative to `path`. With no override, the Action reads
`path/cloudforge.yaml`. Current runtime configuration versions are v1alpha1
through v1alpha7; feature availability depends on the selected version.
Current verification reports use v1alpha8 and legitimate v1alpha1–v1alpha8 saved
reports remain readable. See [runtime configuration](runtime-configuration.md).

Artifact names must be nonempty and valid for GitHub. Retention is 1–90 days,
defaulting to 14. Completed reports are uploaded even when verification fails,
and the Action preserves the CLI exit code. Artifacts also retain native stderr,
the original CLI exit record, binary version identity and candidate manifest.
Outputs are `report-json`,
`report-markdown`, `binary` and `exit-code`. Keep the default
`cloudforge-verification` artifact name when using the bundled PR reporter.

Planner `SUPPORTED` is a scheduling disposition, not product maturity. The
[authoritative support table](supported-applications.md) separates the qualified
HTTP core from experimental backend, worker, controlled and numerical features.

## Baseline artifacts

Use either `baseline-path` or `baseline-run-id`, not both. A local path names a
previously obtained trusted JSON report. A workflow run ID must be a positive
JavaScript-safe integer and requires an explicit read-only `github-token` with
`actions: read`; `baseline-artifact-name` defaults to `cloudforge-verification`.
Only a trusted workflow should select the run ID or provide a token. Choose a
successful trusted default-branch run, never an artifact source selected by
untrusted pull-request code. The download receives the token; verification does
not. The complete bounded saved report is validated before repository execution,
including rejection of duplicate JSON object keys.

## Pull request reporting and trust

Dockerfiles and application code execute in the verification job. Give that job
no write token, production credentials or secrets, and use a disposable runner.
Runtime network policy does not make Docker build execution a hostile-code sandbox.

The separate [`cloudforge-report.yml`](../.github/workflows/cloudforge-report.yml)
workflow is loaded from the trusted default branch. It downloads the JSON artifact
as untrusted data, builds its trusted renderer, rejects malformed/oversized reports,
and regenerates Markdown. It never executes the pull request's code or trusts its
uploaded Markdown. The reporter has only `actions: read`, `contents: read` and
`pull-requests: write`. It updates one authenticated bot-owned comment and removes
bot-owned duplicates; concurrency is grouped by pull request.

Normal reports include summary evidence and bounded findings. An oversized comment
falls back to a compact summary, important evidence and a trusted workflow link
for full artifacts. Full JSON remains complete. Escaping follows one safe path,
so ordinary apostrophes, quotes and Unicode remain readable.

Repository owners choose the trusted reporter revision independently from the
application verification revision. Historical report rendering does not assign
new scan metadata, application identities or numerical verdicts to old evidence.
