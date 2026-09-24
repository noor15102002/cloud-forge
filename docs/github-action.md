# GitHub Action

The released composite Action supports GitHub-hosted Linux/amd64 runners. The
examples below pin the exact [v0.1.0-alpha.1 prerelease](https://github.com/noor15102002/cloud-forge/releases/tag/v0.1.0-alpha.1)
source commit recorded in `release.json`:
`90f1c3c1560d4360b8ec90806154f65ea3d3d5a0`. It installs the
checksum-verified release archive, checks version/full commit/build date, installs
pinned runtime tools, verifies one application and uploads JSON and Markdown.
It does not rebuild CloudForge during a released invocation.

Save this first workflow as `.github/workflows/cloudforge.yml` in the application
repository. The literal full commit in `uses` matches this prerelease;
GitHub Actions does not expand a variable there. Keep the workflow name when
adding the reporter below.

```yaml
name: CloudForge verification
on:
  pull_request:
permissions:
  contents: read
jobs:
  verify:
    runs-on: ubuntu-latest
    timeout-minutes: 40
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: noor15102002/cloud-forge@90f1c3c1560d4360b8ec90806154f65ea3d3d5a0
        with:
          release-version: v0.1.0-alpha.1
          path: .
          artifact-name: cloudforge-verification
```

A missing archive, checksum mismatch or mismatch between Action pin and binary producer stops
execution. The Linux/amd64 runtime tool set is k3d 5.9.0, kubectl 1.35.5,
Trivy 0.74.0 and k6 2.2.0; downloads are checked against upstream checksums.
Docker Engine and its system Buildx plugin are supplied by the disposable hosted
runner and their versions are observed. The default local Unix Docker endpoint
is required. See [first run](first-run.md) for local installation and compatibility
rules. A verification Action does not change the user's Docker context or use
personal scanner policy.

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

Planner `SUPPORTED` is a scheduling disposition, not product maturity. Capability
maturity is shown separately. The
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

Save this second workflow as `.github/workflows/cloudforge-report.yml` on your
repository's **trusted default branch**. Its `workflows` entry exactly matches
`name: CloudForge verification` above. If you rename one, change the other. Keep
the default `cloudforge-verification` artifact name in both workflows. The
repository's own internal reporter is tied to its integration workflow and is
not the consumer template.

The trusted reporter below is pinned to the same released CloudForge commit.
Review and update that trust decision explicitly when adopting a later release.
This external subaction reference works from the consumer repository without a
local copy of CloudForge or its reporter.

```yaml
name: CloudForge pull request report
on:
  workflow_run:
    workflows: [CloudForge verification]
    types: [completed]
permissions:
  actions: read
  contents: read
  pull-requests: write
concurrency:
  group: cloudforge-report-${{ github.event.workflow_run.pull_requests[0].number || github.event.workflow_run.id }}
  cancel-in-progress: true
jobs:
  report:
    if: ${{ github.event.workflow_run.event == 'pull_request' && github.event.workflow_run.pull_requests[0].number != null }}
    runs-on: ubuntu-latest
    timeout-minutes: 15
    steps:
      - name: Download the completed run's report as untrusted data
        uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1
        with:
          name: cloudforge-verification
          path: ${{ runner.temp }}/cloudforge-artifact
          github-token: ${{ github.token }}
          run-id: ${{ github.event.workflow_run.id }}
      - name: Validate and publish the report
        uses: noor15102002/cloud-forge/.github/actions/report@90f1c3c1560d4360b8ec90806154f65ea3d3d5a0
        with:
          report-path: ${{ runner.temp }}/cloudforge-artifact/verification.json
          pull-request-number: ${{ github.event.workflow_run.pull_requests[0].number }}
          github-token: ${{ github.token }}
```

The root verification Action installs the published archive. The ordinary trusted
reporter separately builds a rendering-only executable from its pinned CloudForge
source using Go 1.27.1; it does not rebuild or replace the published runtime
archive. Internal qualification can instead supply the explicit canonical
candidate inputs to the reporter.

The reporter definition comes from the trusted default branch and its renderer
comes from the pinned CloudForge source. It checks no application code out,
downloads only the triggering run's artifact as untrusted data, rejects malformed
or oversized JSON, and regenerates Markdown. It never executes the pull request's
code or trusts uploaded Markdown. It updates one authenticated bot-owned comment
and removes bot-owned duplicates; concurrency is grouped by pull request.

Reporting runs on completed verification jobs, including failed ones with retained
reports. If verification ended before it could produce an artifact, the download
fails visibly rather than posting fabricated evidence. If GitHub omits the pull
request association from the event, the reporter skips; inspect the verification
run's artifact directly. GitHub must allow Actions to write pull-request comments
in the consumer repository. The verification job itself still has no write token.

Normal reports include summary evidence and bounded findings. An oversized comment
falls back to a compact summary, important evidence and a trusted workflow link
for full artifacts. Full JSON remains complete. Escaping follows one safe path,
so ordinary apostrophes, quotes and Unicode remain readable.

Repository owners choose the trusted reporter revision independently from the
application verification revision. Historical report rendering does not assign
new scan metadata, application identities or numerical verdicts to old evidence.
