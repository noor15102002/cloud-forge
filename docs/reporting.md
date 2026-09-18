# Verification Reports

`cloudforge verify` writes one report to standard output. Operational logs stay
on standard error, so reports can be redirected without contamination.

`cloudforge report <verification.json> --format text|json|markdown` validates a
saved report against the bounded `v1alpha1` contract and renders it without
analyzing, building, or executing repository code. The trusted GitHub reporter
uses this command for pull-request artifacts. Its exit status describes loading
and rendering only; the original run and comparison statuses remain in the
report.

## JSON

Use `--format json` for automation. JSON is the canonical `v1alpha1` contract;
its schema is [`schemas/verification.v1alpha1.schema.json`](../schemas/verification.v1alpha1.schema.json).
CloudForge sorts evidence, measurements, findings, and diagnostics before
serialization. Consumers must use field names and must not depend on object key
order.

## Baselines and regressions

Save a canonical report from a trusted default-branch run and pass it explicitly
to a later verification:

```console
cloudforge verify . --format json > baseline.json
cloudforge verify . --baseline baseline.json --format json > current.json
```

The baseline must be a regular `v1alpha1` JSON file no larger than 4 MiB.
CloudForge validates the complete document against the embedded public schema
and rejects incompatible versions, missing required fields, unknown fields,
duplicate experiment IDs, and duplicate measurement names before building or
executing the application. The embedded and public schema copies are checked
for exact equality in tests. This keeps artifact selection and authentication
outside the core engine, so a local command or CI workflow can download the
intended file.

The optional `comparison` object records the baseline run ID, its own status,
regressions, improvements, and unavailable comparisons. The current run's
`status` and `findings` remain absolute observations and are never rewritten by
the relative comparison. A current run can therefore have `status: pass` and a
`comparison.status: fail`. The command exits `1` when either verification fails
or the comparison contains a regression.

Status changes use `pass`, `warn`, `fail`, and `error` severity order. A skipped
experiment is unavailable for status comparison. Numeric measurements are
compared only when CloudForge defines whether lower or higher is better. These
include errors, failures, vulnerabilities, downtime, lifecycle and latency
durations, request count, and throughput. Configuration and descriptive
measurements are retained without assigning them a quality direction. Missing,
nonnumeric, and unit-incompatible measurements appear under `unavailable`.
Reports for different named applications are not compared and produce one
explicit unavailable comparison.

Any change in failure counts, error rate, restarts, vulnerabilities, or downtime
is significant. Latency, other lifecycle durations, request count, and
throughput use a fixed 10 percent relative tolerance to avoid classifying
ordinary runtime noise as a regression or improvement. A nonzero change from a
zero baseline remains significant.

## Terminal

The default `--format text` output shows the overall status, one line per
experiment, baseline changes when requested, actionable findings, and
diagnostics. Detailed measurements remain available in JSON and Markdown so
interactive output stays concise.

## Markdown

Use `--format markdown` to produce a self-contained report suitable for a pull
request comment. It begins with the stable
`cloudforge-verification-report:v1alpha1` marker, summarizes every experiment,
and places measurements and up to 25 detailed findings in collapsible sections. Dynamic text
is escaped to prevent repository metadata from introducing links, mentions, or
HTML into the rendered comment. Markdown shows at most 25 findings and directs
readers to the canonical JSON when more exist; terminal output shows at most 10
actionable findings.
Baseline comparisons add separate regression, improvement, and unavailable
sections. Display limits never truncate the canonical JSON data.
