# Development

CloudForge uses Go 1.27 with the Go 1.27.1 toolchain. Install dependencies with
`go mod download`, then use:

```console
make test
make vet
make lint
make vuln
make build
```

The target application analyzer does not require Docker or Kubernetes tools.
`cloudforge doctor` reports which tools are available without altering the
machine.

Runtime verification requires access to a Docker daemon plus `k3d` and
`kubectl`. Run `cloudforge verify testdata/healthy-node` for the local smoke
test. The command creates and removes its own uniquely named cluster.

Keep operating-system commands behind `internal/command`. Keep public JSON
changes explicit and versioned. Add packages only when they implement current
behavior.
