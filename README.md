# CloudForge

CloudForge is an open-source CLI for proving how containerized applications
behave under production-like Kubernetes conditions. It combines deterministic
repository understanding with measured runtime experiments and evidence-based
regression reporting.

> **Current status:** CloudForge implements environment diagnostics, read-only
> repository analysis, and a first Docker-to-k3d readiness verification path.
> Analysis includes normalized container and Kubernetes configuration findings;
> verification adds normalized Trivy vulnerability findings. Broader experiments,
> reports, baselines, and the distributable GitHub Action remain planned.

## Why CloudForge

Builds and static checks cannot show whether readiness gates traffic, SIGTERM
drops requests, a replacement pod recovers promptly, or a rollout introduces
downtime. CloudForge is being built to run these scenarios in disposable k3d
clusters and report the observed evidence.

```mermaid
flowchart LR
    Repo[Repository] --> Analyzer
    Analyzer --> Model[Architecture model]
    Model --> Planner[Risk and experiment planner]
    Planner --> Executor[Local k3d executor]
    Executor --> Evidence
    Evidence --> Reporters[Terminal / JSON / Markdown]
```

## Install from source

CloudForge currently requires Go 1.27:

```console
go install github.com/noor15102002/cloud-forge/cmd/cloudforge@latest
```

For development, clone the repository and run `make build`. Runtime verification
will require Docker, k3d, kubectl, k6, and Trivy; repository analysis does not.

## Commands

```console
cloudforge version
cloudforge doctor
cloudforge doctor --format json
cloudforge analyze .
cloudforge analyze ./services/api --format json
cloudforge verify .
cloudforge verify ./services/api --format json
```

`analyze` supports application roots containing Node.js, TypeScript, or Python
metadata, root Dockerfiles, and plain Kubernetes Deployment, Service, and HPA
manifests. It recognizes Helm and Compose but does not render or analyze them
yet. It reports source-linked findings for container users and ports, probes,
replicas, resources, Service ports, and HPA ranges. It does not execute
repository code.

JSON output uses the versioned `v1alpha1` schema. Collections are sorted for
repeatable output; consumers must not depend on JSON object key ordering.

`verify` builds the root Dockerfile, creates a uniquely named k3d cluster,
imports the image, deploys a generated Namespace, Deployment, and Service, and
records build and readiness evidence. When an HTTP readiness endpoint is
declared, it measures startup and readiness status, then deletes one ready pod
while sending continuous traffic and records replacement time, failed requests,
downtime, restarts, and final health. It deletes the cluster after success,
failure, timeout, or cancellation. Use `--keep-environment` only when you need
to inspect the cluster manually. Verification executes Dockerfile instructions
and application code, and scans the built image with Trivy. Use it only with
repositories you trust.

This slice reports detected vulnerabilities as warnings while preserving
Trivy's lowercase severity. A configurable blocking policy belongs to the
later regression and reporting work.

## Planned verification experiments

- Graceful shutdown and rolling deployment behavior
- Rolling deployments under continuous traffic
- Deterministic k6 load profiles
- HPA behavior when autoscaling is configured
- Baseline-to-pull-request regression comparison

The planned GitHub Action will upload reports and update one stable pull request
comment. It is not included in the current slice.

## Security and limitations

Analysis reads bounded metadata files, skips generated directories and
symbolic links, and does not load environment files or return Secret values.
Verification builds and runs repository code, which must be treated
as untrusted outside an isolated environment. See [SECURITY.md](SECURITY.md)
and [docs/security.md](docs/security.md).

Linux and WSL2 are the primary targets. See [the roadmap](docs/roadmap.md),
[architecture](docs/architecture.md), and [contribution guide](CONTRIBUTING.md).

Apache-2.0 licensed.
