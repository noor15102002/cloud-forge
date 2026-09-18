# Architecture

CloudForge uses explicit boundaries between repository discovery, architecture
models, future risk planning, execution, evidence, regression comparison, and
reporting.

The implemented slice contains:

- `internal/cli`: command definitions and exit semantics
- `internal/analyzer`: bounded repository traversal and structured parsers
- `internal/command`: the only subprocess execution boundary
- `internal/doctor`: local prerequisite checks
- `internal/executor`: Docker, k3d, kubectl, and Trivy command adapters
- `internal/findings`: deterministic container and Kubernetes configuration checks
- `internal/verification`: generated workload planning and lifecycle orchestration
- `internal/render`: text and JSON serialization
- `pkg/model`: versioned output contracts

The analyzer never executes repository code. It uses structured JSON and TOML
parsing, the BuildKit Dockerfile parser, and official Kubernetes API types for
supported resources. Unsupported Kubernetes resources retain only identity and
provenance metadata.

```mermaid
flowchart LR
    CLI --> Analyzer
    CLI --> Doctor
    CLI --> Verification
    Doctor --> Runner[Command runner]
    Verification --> Analyzer
    Verification --> Executor[Docker / k3d / kubectl / Trivy]
    Analyzer --> Findings[Normalized findings]
    Executor --> Findings
    Executor --> Runner
    Analyzer --> Model[Versioned models]
    Doctor --> Model
    Model --> Render[Text / JSON renderer]
```

Verification generates one narrowly scoped Kubernetes workload from
unambiguous analyzed metadata. A fresh k3d cluster isolates every run. Cleanup
uses a separate bounded context so cancellation of the experiment does not
cancel deletion.
