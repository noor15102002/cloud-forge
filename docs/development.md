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

Use `make release RELEASE_VERSION=v0.1.0-alpha.1` only from a clean committed
checkout to create a reproducible candidate with full producer identity. Follow
[release qualification](releasing.md) before distributing it. `make build` and
local Action source builds remain development builds.

Run `make external` when changing analysis behavior. It builds CloudForge,
fetches exact revisions from the documented public repository matrix, and runs
read-only analysis without building or executing those applications.

The target application analyzer does not require Docker or Kubernetes tools.
`cloudforge doctor` reports which tools are available without altering the
machine.

Runtime verification requires the supported local Docker Engine plus its system
Buildx plugin, `k3d`, `kubectl`, `k6` and Trivy. Follow the complete prerequisite
and tool-version instructions in [first run](first-run.md). Use
`cloudforge verify examples/http-core` for the local core smoke test. The command
creates and removes its own uniquely named cluster. The older
`testdata/healthy-node` fixture additionally enables experimental HPA and control
protocols, so it is not the first-use core recipe.

Keep the selected application checkout unchanged throughout verification. Both
lifecycle builds read it separately; CloudForge does not snapshot source files.

Action development also requires Node.js and `actionlint`. `make actions` tests
the stable comment updater, rejects unsafe input combinations, and validates the
workflow and composite-action metadata. The runtime integration workflow then
exercises the root action on a clean Ubuntu runner.

Keep operating-system commands behind `internal/command`. Keep public JSON
changes explicit and versioned. Add packages only when they implement current
behavior.
