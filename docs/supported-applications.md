# Supported applications

| Workload | Analysis | Runtime |
| --- | --- | --- |
| Single stateless HTTP container | Node/TypeScript/Python metadata | Supported within the documented deployment contract |
| HTTP + explicitly declared Redis | Redis client hints plus explicit test requirements | Supported with the first-party isolated Redis provider |
| HTTP + PostgreSQL | Supported client hints; generic ORM remains unknown | BLOCKED when required; no provider |
| HTTP + Redis + PostgreSQL | Partial dependency hints | BLOCKED when PostgreSQL is required |
| Worker-only service | Metadata may be recognized | No supported HTTP verification contract |
| Database as the application | Not a supported application family | Unsupported |
| Mobile / desktop | Not supported | Unsupported |
| Arbitrary Compose / Helm topology | Detected within selected root only | Not executed |

Recognition does not prove boot requirements. Explicit configuration overrides
safe deterministic discovery; unresolved dependency requirements block execution.
Use `verify --plan` to inspect supported, skipped and blocked capabilities before
building. Load remains bounded GET only; optional controlled tests require the
published instrumentation protocol. See [dependency runtime](dependency-runtime.md)
and [runtime configuration](runtime-configuration.md).
