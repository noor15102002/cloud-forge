# Single-workload build selection

CloudForge can inspect and verify one HTTP workload inside a monorepo. It does
not start sibling services or orchestrate a workspace. Selection is explicit:

```yaml
schema_version: v1alpha3
build:
  app: apps/http
  dockerfile: apps/http/Containerfile.release
  context: .
runtime:
  port: 8080
endpoints:
  health: /health
  readiness: /ready
```

Run `cloudforge analyze .`, `cloudforge verify . --plan`, then
`cloudforge verify . --format json` from the repository root. These commands
read `cloudforge.yaml` by default. `--config /path/to/pilot.yaml` lets you keep
the configuration outside the source checkout; selection paths still resolve
against the positional repository argument.

- `app` selects Node/Python metadata and plain Kubernetes files. Sibling apps
  are excluded. Source references are repository-relative.
- `dockerfile` selects exactly one regular file, regardless of its filename.
  Other Dockerfiles are excluded from this workload's findings.
- `context` selects Docker COPY/ADD inputs. A repository-root context can include
  shared packages outside the app. Docker's `.dockerignore` behavior applies.

All three fields are required when `build` is present. They must exist within
the repository: no absolute paths, URLs, `..` components, `.git`, option-like
components or symlink components. Paths are bounded to 512 bytes; the Dockerfile
is bounded to 2 MiB. A directory named `apps/my app` is passed as one argument.
The Dockerfile may sit outside the context but must stay inside the repository.
Shell variables and glob expansion are not supported.

The selected tuple is normalized, shown in the plan, and used for both image A
and image B. It is rechecked before each Docker invocation. Repository Git
identity and the effective selection are fingerprinted; different selections
cannot be silently compared as the same environment.

Without `build`, root Dockerfile selection and earlier configuration behavior
are unchanged. Runtime v1alpha1/v1alpha2 configuration remains supported.
Selection uses v1alpha3 configuration, v1alpha2 analysis and v1alpha4
verification/plan schemas. Previously saved reports remain readable.

Limitations: one workload, one Dockerfile, no build-target/build-secret/arbitrary
build-argument contract, no Compose or Helm execution, and no inference of
production secrets. Kubernetes manifests must be under the selected app.
A broad context does not imply CloudForge has analyzed every source file copied
by Docker. Deterministic plans describe inputs; they do not make builds
reproducible when base tags, network dependencies or the checkout change.

The runnable `testdata/monorepo` fixture demonstrates a custom filename and a
root context required to import `packages/shared`. Its semantic readiness checks
the shared value. It opts out of load and control-protocol experiments because
no representative workload or instrumentation is configured.

See the [runtime acceptance record](build-selection-evidence.md) for measured
results, preserved failures and current limitations.
