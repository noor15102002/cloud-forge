# Product Definition

CloudForge answers one question: can this change behave safely under
production-like Kubernetes conditions?

It will analyze supported containerized applications, select relevant risks,
run controlled experiments in disposable environments, normalize observations
as evidence, compare runs, and report regressions locally and on pull requests.

The current release slice provides deterministic analysis and environment
diagnostics. Runtime verification remains on the roadmap.

CloudForge is not a deployment platform, CI system, static Kubernetes linter,
generic security scanner, hosted dashboard, or AI decision engine.

