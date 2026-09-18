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
arbitrary code through its Dockerfile and container entry point. Local users
should run it only for repositories they trust. The GitHub verification job
uses no repository secrets, has read-only repository permissions, and checks
out pull requests without persisted credentials. Fork pull requests do not
receive write credentials or privileged infrastructure.

Pull-request comments use a separate `workflow_run` job whose definition and
reporter code come from the trusted default branch. That job receives only
`actions: read`, `contents: read`, and `pull-requests: write`. It downloads the
pull-request artifact as untrusted data, validates the complete bounded JSON
schema, and regenerates escaped Markdown before calling GitHub. It never
executes the pull request's Dockerfile, scripts, binary, or uploaded Markdown.
The standard GitHub token is passed only to official artifact download and
comment steps.

Cross-run baseline download is opt-in and requires a caller-selected run ID and
an explicit read-only token. Use it only from a trusted pinned workflow, and
select successful default-branch runs. The token is scoped to the download step
and is not passed to application verification.

The public Action canonicalizes the application directory and requires it to
remain inside the checked-out workspace. It rejects malformed artifact names,
out-of-range retention periods, unsafe run IDs, and incomplete baseline inputs
before installing tools or running CloudForge. Token validation passes only a
presence flag to the shell; the token value is not placed in a shell
environment.

CloudForge generates a Namespace, Deployment, and Service from analyzed
metadata. It does not apply source manifests or copy Secret, ConfigMap, or
environment values into the generated workload. Independent cleanup contexts
delete the cluster after success, failure, timeout, or cancellation unless the
operator explicitly passes `--keep-environment`. Each image and cluster removal
gets its own bounded context, so one failed removal cannot consume the timeout
for later resources. A partially created cluster is always deleted; the keep
flag applies only after cluster creation succeeds.

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

Markdown reports escape repository-derived text, inline HTML, links, and
mentions before producing content intended for a pull-request comment. JSON
retains the original normalized values and should be treated as data by
consumers.
