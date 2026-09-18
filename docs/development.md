# Development

CloudForge uses Go 1.27 with the Go 1.27.1 toolchain. Install dependencies with
`go mod download`, then use:

```console
make test
make vet
make lint
make vuln
make actions
make build
```

Run `make external` when changing analysis behavior. It builds CloudForge,
fetches exact revisions from the documented public repository matrix, and runs
read-only analysis without building or executing those applications.

The target application analyzer does not require Docker or Kubernetes tools.
`cloudforge doctor` reports which tools are available without altering the
machine.

Runtime verification requires access to a Docker daemon plus `k3d`, `kubectl`,
`k6`, and Trivy. Run `cloudforge verify testdata/healthy-node` for the local
smoke test. The command creates and removes its own uniquely named cluster.

Action development also requires Node.js and `actionlint`. `make actions` tests
the stable comment updater, rejects unsafe input combinations, and validates the
workflow and composite-action metadata. The runtime integration workflow then
exercises the root action on a clean Ubuntu runner.

Keep operating-system commands behind `internal/command`. Keep public JSON
changes explicit and versioned. Add packages only when they implement current
behavior.
