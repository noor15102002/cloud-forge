# Product Definition

CloudForge answers one question: can this change behave safely under
production-like Kubernetes conditions?

It will analyze supported containerized applications, select relevant risks,
run controlled experiments in disposable environments, normalize observations
as evidence, compare runs, and report regressions locally and on pull requests.

The current release slice provides deterministic analysis, normalized
container, Kubernetes, and Trivy findings, environment diagnostics, and
measured container-build and Deployment-readiness evidence in an isolated k3d
cluster. It also measures HTTP readiness, pod replacement, graceful shutdown,
and version-to-version rolling deployment behavior under continuous traffic.
It also runs a bounded deterministic k6 profile and observes CPU-based HPA
scale-up when the repository declares one. Results are available as concise
terminal output, canonical versioned JSON, or pull-request-ready Markdown.
An explicit previous report can be supplied to classify status and measurement
regressions, improvements, and unavailable comparisons without coupling the
core engine to an artifact provider.
The GitHub Action packages this flow for Ubuntu, uploads both report formats,
and publishes one stable pull-request comment through a separate trusted
reporting workflow.

CloudForge is not a deployment platform, CI system, static Kubernetes linter,
generic security scanner, hosted dashboard, or AI decision engine.
