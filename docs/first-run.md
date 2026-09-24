# First run: one HTTP service

Use these installation commands only after the prerelease and its exact archive
qualification record are public. A development candidate is not a published
release, and an earlier archive's qualification does not transfer to changed
bytes. For current release status, see
[project status](project-status.md).

This path exercises only the bounded HTTP core. It does not opt into HPA,
controlled experiments, workers, backend preparation or numerical regression
grading. Use a trusted source checkout and disposable test machine. The
[execution boundary](security.md) includes real Docker builds and application code.

## Machine and tools

Use Linux on amd64 with a local Docker Engine available to the current user at
`unix:///var/run/docker.sock`. Approximately 16 GiB RAM is recommended; below
8 GiB the doctor reports a warning. The 4 GiB test cluster and 2 GiB build limits
are bounded components, not a reservation or guarantee for total host use. Leave
disk space and memory for image downloads and the host's other processes.

Remote Docker endpoints, Docker Desktop contexts, rootless/custom sockets and
TLS-based Docker selection are outside this alpha's supported endpoint contract.
CloudForge must reject them before runtime mutations rather than switch engines.
It does not change the user's selected Docker or Kubernetes context. Use the
default local engine deliberately, or choose a separate supported machine;
do not clear an existing context setting just to silence a prerequisite finding.

Install Docker Engine and its Buildx plugin using the
[official Linux installation guide](https://docs.docker.com/engine/install/).
For Ubuntu with Docker's official apt repository already configured, the packages
are:

```sh
sudo apt-get update
sudo apt-get install docker-ce docker-ce-cli containerd.io docker-buildx-plugin
docker version
docker buildx version
docker info --format '{{.ServerVersion}}'
```

The last commands must work as the user who will run CloudForge. Follow Docker's
documented daemon-access setup if access is denied; do not make its socket
world-writable or run an unfamiliar application as root to bypass the problem.
CloudForge uses a private Docker configuration and builder. A manually installed
per-user Buildx plugin is not the supported installation path: install the system
plugin so it remains available with the private configuration.

| Tool | Reference bundle / policy |
| --- | --- |
| Docker Engine | 28.0.4 is the reference compatibility version; the actual daemon version is recorded. |
| Docker Buildx | Required system-installed plugin; its observed version is recorded as `NOT_VALIDATED` because no version has an independent supported pin. |
| k3d | 5.9.0 |
| Kubernetes node image | `rancher/k3s:v1.35.5-k3s1`, selected by CloudForge |
| kubectl | 1.35.5; the client must remain within one minor of the server |
| k6 | 2.2.0 |
| Trivy | 0.74.0 |

`SUPPORTED`, `UNSUPPORTED` and `NOT_VALIDATED` compatibility are distinct from
application findings and capability maturity. A different version is not
automatically broken or qualified. The doctor and runtime preflight apply the
same compatibility policy; the release evidence records the tuple actually used.
Known incompatibility blocks execution, and an unobservable version is an
execution error. `analyze` and `verify --plan` do not require these tools.

## Install the archive and matching example

The following commands use `curl`, `tar`, `sha256sum`, Python 3 and Git. Run them
in an empty directory. They install the release binary and check out the matching
source only to obtain the example and pinned runtime-tool installer. No Go or
local Node installation is needed to use the release binary.
Run the installation sequence in the same shell. `set -eu` stops it immediately
if a download, checksum, identity check or installation step fails; resolve that
failure before continuing.

```sh
set -eu
version=v0.1.0-alpha.1
archive="cloudforge_${version}_linux_amd64.tar.gz"
base="https://github.com/noor15102002/cloud-forge/releases/download/$version"
curl -fLO "$base/$archive"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/release.json"
sha256sum --check checksums.txt
tar -xzf "$archive" cloudforge
python3 -c 'import hashlib,json,pathlib; m=json.load(open("release.json")); assert m["version"]=="v0.1.0-alpha.1" and m["os"]=="linux" and m["arch"]=="amd64"; assert hashlib.sha256(pathlib.Path("cloudforge").read_bytes()).hexdigest()==m["binary_sha256"]'
install -Dm755 cloudforge "$HOME/.local/bin/cloudforge"
export PATH="$HOME/.local/bin:$PATH"
cloudforge version

git clone --branch "$version" --depth 1 https://github.com/noor15102002/cloud-forge.git source
test "$(git -C source rev-parse HEAD)" = "$(python3 -c 'import json; print(json.load(open("release.json"))["commit"])')"
```

Compare the binary's printed full commit and version with `release.json`. Keep
that manifest and `checksums.txt` with the downloaded archive.

Install the pinned k3d, kubectl, k6 and Trivy binaries in a user-owned directory:

```sh
tool_root="$HOME/.local/share/cloudforge/tools/$version"
bash source/scripts/install-tools.sh --local "$tool_root"
export PATH="$tool_root/bin:$PATH"
cloudforge doctor
```

The checked-in installer verifies upstream download checksums and does not
install or reconfigure Docker. Retain the PATH additions for later sessions.
Resolve prerequisite failures before verification. A doctor warning such as
`NOT_VALIDATED` is useful information, not evidence that your environment matches
the qualified one.

Allow outbound access for the pinned tool downloads, public container images and
Trivy's vulnerability database. Each run uses a private scanner configuration
and fresh cache so an existing scanner setting or ignore rule cannot silently
change its policy. Database retrieval must complete within the bounded scan
deadline; an unavailable database or unusable scan is an execution error, not a
clean security result. Offline runtime verification is not qualified.

## Inspect, then execute

```sh
cloudforge analyze source/examples/http-core
cloudforge verify source/examples/http-core --plan
if cloudforge verify source/examples/http-core --format json > verification.json; then
  verify_exit=0
else
  verify_exit=$?
fi
printf 'CloudForge exit code: %s\n' "$verify_exit"
cloudforge report verification.json --format text
cloudforge report verification.json --format markdown > verification.md
```

The first two commands only inspect the repository. Confirm that the plan shows
two source replicas, readiness, bounded load and the core lifecycle checks.
HPA, controlled readiness gating and targeted in-flight shutdown should be
`SKIPPED` with a reason. The example declares no Redis requirement. To evaluate
Redis later, use the explicit [dependency contract](dependency-runtime.md).

`verify` builds and scans image A, runs the isolated application, measures
readiness, replacement, recovery and same-source rollout, runs five virtual users
for twenty seconds, restores the baseline between mutations and attempts owned
cleanup. Keep the checkout unchanged for the entire run: image A and image B read
the live checkout separately. CloudForge does not make a frozen source snapshot.

Keep the native JSON even when the command exits nonzero. `PASS` or `WARN` exit
zero; a measured requirement failure or blocked prerequisite exits one;
CloudForge execution/observation errors exit two. A scanner warning can be a
valid completed scan with vulnerabilities found. A failure followed by successful
restoration remains a failure. Cleanup evidence is separate and must be checked.
Do not expect or force every measured experiment to PASS.

The example's `/work` response is an onboarding workload, not an application
business flow or a representative performance benchmark. Two replicas provide a
test topology, not a continuous-availability guarantee. See [reporting](reporting.md)
for the precise meaning of sampled downtime and each evidence status.

If interrupted, allow bounded cleanup to finish. If cleanup reports an error,
follow the exact named-resource instructions in [security](security.md); do not
use a broad Docker prune. For a consumer repository, the next step is the
[GitHub Action setup](github-action.md), using an immutable released commit.
