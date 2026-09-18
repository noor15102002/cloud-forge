# Security and Trust Model

Repository analysis is read-only. CloudForge resolves the selected root,
does not follow repository symbolic links, skips common generated directories,
limits traversal to 2,000 files, and limits each parsed file to 2 MiB.

It does not parse `.env` files, expose dependency versions, or return the data
portion of Kubernetes resources. Unknown resources are represented only by API
version, kind, name, namespace, and source location. Command output used by
`doctor` is bounded and reduced to a short sanitized version line.

Verification executes Docker builds and application code in a disposable local
k3d cluster. Operators must assume that a verified repository can execute
arbitrary code through its Dockerfile and container entry point. Run it only on
trusted repositories. The integration workflow uses no repository secrets and
has read-only repository permissions; fork pull requests must not receive
credentials or privileged infrastructure.

CloudForge generates a Namespace, Deployment, and Service from analyzed
metadata. It does not apply source manifests or copy Secret, ConfigMap, or
environment values into the generated workload. A separate cleanup context
deletes the cluster after success, failure, timeout, or cancellation unless the
operator explicitly passes `--keep-environment`.

Runtime HTTP experiments publish the generated Service only on a dynamically
selected `127.0.0.1` port. CloudForge reads at most 1 KiB of each response body
and does not include response content in evidence or diagnostics. The HTTP
transport ignores proxy environment variables for these loopback requests.

Rolling-deployment verification rebuilds the trusted repository with the
non-secret `CLOUDFORGE_VERSION` build argument set to `a` and `b`. CloudForge
does not pass environment variables or credentials into either Docker build.

Load verification writes a generated k6 script containing only the loopback
endpoint into the run's temporary directory. Profiles are capped at 64 virtual
users and one minute; the default is 16 users for 20 seconds. Generated HPAs
are capped at five replicas to bound local resource use. Neither raw response
bodies nor source environment values enter the load report.

Trivy runs against the locally built image. CloudForge parses bounded JSON and
retains vulnerability identifier, package, installed version, fixed version,
severity, and image target metadata. Raw scanner output is not included in the
public report.

Vulnerability findings are warnings in this slice, with the scanner severity
preserved separately. CloudForge does not yet impose a vulnerability threshold.

Subprocesses use executable and argument arrays rather than shell command
strings. Logs must never contain tokens, credentials, or full environments.
