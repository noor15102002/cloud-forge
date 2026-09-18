# Verification Reports

`cloudforge verify` writes one report to standard output. Operational logs stay
on standard error, so reports can be redirected without contamination.

## JSON

Use `--format json` for automation. JSON is the canonical `v1alpha1` contract;
its schema is [`schemas/verification.v1alpha1.schema.json`](../schemas/verification.v1alpha1.schema.json).
CloudForge sorts evidence, measurements, findings, and diagnostics before
serialization. Consumers must use field names and must not depend on object key
order.

## Terminal

The default `--format text` output shows the overall status, one line per
experiment, actionable findings, and diagnostics. Detailed measurements remain
available in JSON and Markdown so interactive output stays concise.

## Markdown

Use `--format markdown` to produce a self-contained report suitable for a pull
request comment. It begins with the stable
`cloudforge-verification-report:v1alpha1` marker, summarizes every experiment,
and places measurements and up to 25 detailed findings in collapsible sections. Dynamic text
is escaped to prevent repository metadata from introducing links, mentions, or
HTML into the rendered comment. Markdown shows at most 25 findings and directs
readers to the canonical JSON when more exist; terminal output shows at most 10
actionable findings.
