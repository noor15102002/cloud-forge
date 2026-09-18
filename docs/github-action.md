# GitHub Action

CloudForge provides a composite action for GitHub-hosted Ubuntu x64 runners.
It installs checksum-verified versions of k3d, kubectl, Trivy, and k6, sets up
Go 1.27.1, builds CloudForge from the selected action ref, runs verification,
and uploads `verification.json` and `verification.md` in one artifact.

Pin the action to a reviewed full commit SHA. Replace the placeholder below
with the release commit selected by your repository:

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
      - uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09 # v5
        with:
          persist-credentials: false
      - uses: noor15102002/cloud-forge@FULL_40_CHARACTER_COMMIT_SHA
        with:
          path: .
          artifact-name: cloudforge-verification
```

The verification action needs only `contents: read`. It does not receive a
GitHub token by default. A failed experiment still uploads any completed
reports and then preserves CloudForge's exit code.

## Baseline artifacts

The action accepts either `baseline-path` or `baseline-run-id`. A local path is
appropriate when the workflow has already obtained or checked out a trusted
baseline. `baseline-run-id` downloads `verification.json` from the configured
`baseline-artifact-name` through the commit-pinned official download action.
It also requires an explicit read-only `github-token` input:

```yaml
      - uses: noor15102002/cloud-forge@FULL_40_CHARACTER_COMMIT_SHA
        with:
          path: .
          baseline-run-id: ${{ inputs.trusted_baseline_run_id }}
          baseline-artifact-name: cloudforge-verification
          github-token: ${{ github.token }}
```

Only trusted workflows should select a baseline run ID or pass a token. Select
a successful run from the repository's default branch; never allow pull-request
code to choose a privileged artifact source. The download step receives the
token, but the later verification step does not. CloudForge validates the
downloaded report against its complete bounded `v1alpha1` schema before any
repository code executes.

## Pull request reporting

Running a Dockerfile means running untrusted code. A pull-request job therefore
must not have write permission or secrets. Comment publication happens in a
separate `workflow_run` workflow loaded from the trusted default branch. The
checked-in [`cloudforge-report.yml`](../.github/workflows/cloudforge-report.yml)
shows the complete pattern:

1. The read-only verification workflow checks out code without persisted
   credentials, executes CloudForge, and uploads reports.
2. The reporting workflow downloads the JSON artifact as untrusted data.
3. It checks out the trusted default-branch renderer and validates the complete
   JSON schema with a 4 MiB input limit.
4. It regenerates escaped, bounded Markdown and uses only
   `actions: read`, `contents: read`, and `pull-requests: write` permissions.
5. It updates the authenticated bot's existing
   `cloudforge-verification-report:v1alpha1` comment and removes bot-owned
   duplicates.

The reporter never executes code from the pull request and never trusts the
uploaded Markdown artifact. Concurrency is grouped by pull request to prevent
two completed runs from racing to create duplicate comments.

## Outputs

The verification action exports `report-json`, `report-markdown`, `binary`, and
`exit-code`. The reports are also uploaded under `artifact-name` for the
configured retention period, which defaults to 14 days.
