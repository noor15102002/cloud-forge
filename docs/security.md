# Security and Trust Model

Repository analysis is read-only. CloudForge resolves the selected root,
does not follow repository symbolic links, skips common generated directories,
limits traversal to 2,000 files, and limits each parsed file to 2 MiB.

It does not parse `.env` files, expose dependency versions, or return the data
portion of Kubernetes resources. Unknown resources are represented only by API
version, kind, name, namespace, and source location. Command output used by
`doctor` is bounded and reduced to a short sanitized version line.

Future verification will execute Docker builds and application code. Operators
must assume that a verified repository can execute arbitrary code. Fork pull
requests must not receive credentials or access privileged verification merely
because they opened a pull request.

Subprocesses use executable and argument arrays rather than shell command
strings. Logs must never contain tokens, credentials, or full environments.

