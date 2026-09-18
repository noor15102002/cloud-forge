# External Compatibility Validation

CloudForge validates its analyzer against public applications in addition to
the purpose-built fixtures. The validation script fetches exact Git commits
with sparse checkouts and runs only `cloudforge analyze`; it never builds or
executes external repository code.

Run the reproducible matrix with:

```console
make external
```

The matrix was last run successfully on 2026-09-18:

| Repository and selected path | Commit | Expected detection |
| --- | --- | --- |
| `dockersamples/node-bulletin-board/bulletin-board-app` | `4aaaa14f11cb9b45fe1a1b9db3edfdb5d9a4a13e` | Node.js and Express |
| `kconner/next-js-in-docker-example` | `a2daae78c5612ee63de97a03491f1291edf3f588` | TypeScript and Next.js |
| `GoogleCloudPlatform/python-docs-samples/run/helloworld` | `5e143effc56aeb9991f86c1d5dc2ba16c2aee4c3` | Python and Flask |
| `fastapi/full-stack-fastapi-template/backend` | `cb740b656d7a0a6c5e12c7bf8e50343ec94ee9c7` | Python, FastAPI, and PostgreSQL |

These checks exercise general structured metadata detection across npm,
`requirements.txt`, and `pyproject.toml` projects. CloudForge does not contain
repository-specific behavior for these applications.

An analysis result marked `supported` means CloudForge understands the
application manifest. Runtime verification additionally requires exactly one
root Dockerfile and one unambiguous TCP port. It can generate a minimal
Deployment and Service when no supported Kubernetes workload exists. External
applications that depend on Compose services, secret configuration, multiple
containers, multiple Deployments, dynamic Dockerfile ports, or monorepo build
context need an explicitly supported configuration before runtime verification.
