# Generic backend qualification fixture

This original Node HTTP fixture requires an actual PostgreSQL `vector` extension,
a prepared row whose vector distance is exactly five, Redis PING, ClamAV PING and
a generated 32-byte test key. It never contacts an external business service.
The production Node image is digest-pinned; `pg` and transitive packages are locked.
The JSON-formatted `cloudforge.yaml` is valid YAML and uses the bounded backend
profile with dependency-only outbound networking.

`prepare.js` creates the extension/table/row before the HTTP application starts.
Omitting preparation or the generated key prevents startup. `--fail` makes
preparation fail deliberately and prints the generated fixture key to container
stderr to check that CloudForge never includes that content in public evidence.
No real credentials or customer data are used.

The application implements the existing explicit `/_test` protocol for readiness
gating and targeted in-flight SIGTERM observations. `/ready` and `/work` query the
real prepared backend dependencies; `/health` is process liveness. Availability
FAILs remain observations and are not rewritten into PASS by the harness.

From the repository root, run static inspection without runtime tools:

```sh
python3 scripts/pilot-backend.py ./bin/cloudforge /tmp/backend-inspection
```

On a disposable GitHub-hosted runner, add `--run` for healthy, preparation-failure,
missing-key and missing-schema cases, or select one with `--case healthy`.
The harness preserves exact CLI stdout, stderr, exit code, canonical/Markdown
reports, actual generated-secret omission checks and cleanup evidence.

The healthy case temporarily adds five small run-owned probe pods and one
control-only policy. A live private canary proves application/preparation/ClamAV
egress to undeclared destinations is denied by the actual CNI while declared
provider ports remain accessible. Positive controls bracket the denied probes.
Probe resources are removed before application startup. ClamAV's public download
allowance and its revocation are checked by actual policy readback; this does not
claim to test public internet connectivity or Kubernetes host/node exceptions.
No Docker commands or Kubernetes creation run in static inspection mode.
