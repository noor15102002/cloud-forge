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
