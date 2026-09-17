# Security Policy

Please report vulnerabilities privately through GitHub's security advisory
feature. Do not include secrets or exploit details in a public issue.

The current analyzer reads repository metadata but does not build or execute
the target application. Future verification commands will execute repository
build and runtime code and must be used only for repositories the operator is
authorized to run, inside an appropriately isolated environment.

See [docs/security.md](docs/security.md) for the trust model and current
controls.

