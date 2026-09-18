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

Subprocesses use executable and argument arrays rather than shell command
strings. Logs must never contain tokens, credentials, or full environments.
