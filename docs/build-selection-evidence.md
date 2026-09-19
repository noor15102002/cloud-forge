# Single-workload build selection acceptance

Implementation: [PR #40](https://github.com/noor15102002/cloud-forge/pull/40),
merged as `7b1a35fd2fc3a469c243e1c799a28ad2898555e8`.

This slice selects one application's metadata, Dockerfile and build context
without adding another scheduler or dependency provider. The reference workload
uses `apps/http/Containerfile.release`, repository-root context and an imported
`packages/shared` package. Its readiness contract checks that shared package's
value, proving that the built service can import the shared workspace package.

## Reproducible evidence

| Gate | Evidence |
| --- | --- |
| Code quality, race tests, vet, lint, vulnerability check, Action syntax and build | [PR CI](https://github.com/noor15102002/cloud-forge/actions/runs/35454163816) |
| Custom Dockerfile, shared workspace, A/B rollout, wrong context, build cancellation and source hashes | [Monorepo acceptance](https://github.com/noor15102002/cloud-forge/actions/runs/35454163834) |
| Existing packaged Action, healthy runtime and planted shutdown/rollout failures | [Runtime integration](https://github.com/noor15102002/cloud-forge/actions/runs/35454163819) |
| Redis Node/Python startup, semantic readiness and cancellation | [Dependency runtime](https://github.com/noor15102002/cloud-forge/actions/runs/35454163826) |
| Same-code topology semantics and restore-and-continue | [Evidence reliability](https://github.com/noor15102002/cloud-forge/actions/runs/35454163818) |
| Repeated references, public apps and real interruption/cleanup | [Pilot matrix, including repeat](https://github.com/noor15102002/cloud-forge/actions/runs/35454163820) |
| Packaged Action on the actual merged implementation revision | [Merged monorepo run](https://github.com/noor15102002/cloud-forge/actions/runs/35466584693) |
| Trusted renderer accepts the new schema after merge | [Reporter repeat](https://github.com/noor15102002/cloud-forge/actions/runs/35454583942) |

The generic monorepo's build, deployment readiness, semantic readiness, replacement,
recovery and rolling deployment passed. Every mutating lifecycle experiment
restored and validated the intended baseline. Load, HPA, controlled readiness
gating and targeted in-flight shutdown remained SKIPPED because the fixture
provides no corresponding contract. The image scan recorded 9 vulnerabilities
as WARN under the existing policy; the overall report was WARN with those
findings retained.

The deliberately app-only build context failed the image build and did not
start deployment. A real interruption of the selected custom Dockerfile build
returned cancellation ERROR; owned resources were removed while a sentinel
container, network, volume and kubeconfig were preserved. Fixture hashes stayed
unchanged. Inspection and saved-report serialization repeated exactly.

Unit and contract checks cover path traversal, symlink components and path swaps,
nonregular and oversized Dockerfiles, arguments containing spaces, independent
config placement, provenance, A/B argument consistency and selection-sensitive
fingerprints. Published analysis/config/plan schemas validate their output;
old verification v1alpha1/v1alpha2/v1alpha3 files remain readable with original
observations preserved.

## Retained failure and limits

The latest PR's first Python attempt stopped on healthy trial 2: 2 of 50 Service
identity requests reached the unready pod after the existing one-second
propagation allowance. This remains a valid recorded FAIL for that observation.
Restoration passed, and shutdown, targeted in-flight shutdown, recovery, rollout
and load subsequently passed. No runtime experiment or threshold was changed.

One unchanged repeat passed all five healthy Python trials and both planted
failures. The earlier implementation commit also passed five healthy Python
trials and both planted failures. The latest Node batch passed its five healthy
trials and planted failures. These results do not erase the failed observation
or establish universal routing stability. The fixed propagation allowance remains
a limit to consider when interpreting readiness-gating evidence; these trials
are not a statistical guarantee.

A selected build context is not a claim that every copied source file was
analyzed. Builds still depend on Docker, `.dockerignore`, image/package
availability and the supplied Dockerfile. Only one workload is deployed, and
plain Kubernetes manifests must be within its selected app directory. Build
secrets, arbitrary build arguments, multiple services, Compose and Helm execution
remain outside this slice.

Private application source and detailed pilot results are retained outside this
public repository. A lifecycle/health pilot does not establish correctness of
login, billing, tenant isolation, AI or other business flows.
