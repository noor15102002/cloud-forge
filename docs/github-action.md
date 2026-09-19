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
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
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

Inputs are validated before tools are installed. `path` must name an existing
directory whose canonical location remains inside the GitHub workspace.
`baseline-path` and `baseline-run-id` are mutually exclusive; a run ID must be
a positive JavaScript-safe integer and requires an explicit token and valid
artifact name. Report artifact names must be nonempty and use GitHub-supported
characters, and retention must be an integer from 1 through 90 days. Token
validation exposes only a presence flag to shell code, never the token value.

## Baseline artifacts

The action accepts either `baseline-path` or `baseline-run-id`. A local path is
appropriate when the workflow has already obtained or checked out a trusted
baseline. `baseline-run-id` downloads `verification.json` from the configured
`baseline-artifact-name` through the commit-pinned official download action.
It also requires an explicit token with `actions: read`. This complete manual
workflow keeps selection of the baseline run in a trusted workflow input:

```yaml
name: CloudForge with baseline

on:
  workflow_dispatch:
    inputs:
      trusted_baseline_run_id:
        description: Successful default-branch Runtime integration run ID
        required: true
        type: string

permissions:
  actions: read
  contents: read

jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
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

The checked-in reporter consumes the default `cloudforge-verification`
artifact name. Keep that default when using the bundled workflow. Repositories
that set `artifact-name` must use the same name in their own trusted reporting
workflow.

## Outputs

The verification action exports `report-json`, `report-markdown`, `binary`, and
`exit-code`. The reports are also uploaded under `artifact-name` for the
configured retention period, which defaults to 14 days.

Dependency-aware runs use the application-root `cloudforge.yaml` v1alpha2 configuration. Reports retain BLOCKED dependency plans and separate dependency outcomes; the trusted reporter accepts all three schema-version markers.

Runtime reports now use v1alpha3; runtime configuration remains v1alpha1/v1alpha2.
Tool compatibility and verifier build identity are checked before application
execution. See [evidence reliability](evidence-reliability.md).

External Action use requires a full 40-character commit SHA pin. Downloaded
Actions may lack Git metadata, so this immutable ref is injected as the verifier
commit; a moving tag or branch is not resolved later and claimed as build
identity. Local `uses: ./` builds retain Go VCS metadata.
