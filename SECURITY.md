# Security Policy

Please report vulnerabilities privately through GitHub's security advisory
feature. Do not include secrets or exploit details in a public issue.

The analyzer reads repository metadata without executing the target
application. The `verify` command builds and runs repository code in Docker and
a disposable k3d cluster, so local operators must use it only for repositories
they are authorized to execute. GitHub pull-request verification uses a
read-only job without secrets; comment publication is isolated in a trusted
default-branch workflow that treats reports as untrusted data.

See [docs/security.md](docs/security.md) for the trust model and current
controls.

## Explicit test dependencies

Redis is pinned by digest, ClusterIP-only, non-root and resource bounded inside
the unique disposable cluster. Persistence and authentication are disabled for
this isolated test mode. No production endpoints or credentials are loaded.
Only generated endpoint bindings and restricted declared harmless literals enter
the application manifest; literals are omitted from reports and hashed for
compatibility. Do not place secrets in those fields. Image-defined environment
remains the application's responsibility. Redis is not an added host service or
a security sandbox, and its image is not separately scanned in this release.

Readiness bodies are bounded to 64KiB, parsed without execution and omitted from
evidence. No shell interpolation, query language, host environment import or
arbitrary Compose execution is supported. See docs/dependency-runtime.md.
