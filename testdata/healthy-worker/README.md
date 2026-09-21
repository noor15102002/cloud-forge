# Bounded worker heartbeat fixture

This is a background process with no HTTP listener or Kubernetes application
Service. It writes a small JSON heartbeat to the managed disposable Redis every
second. The heartbeat has an advancing `at` timestamp and a three-second TTL.
The image is pinned, uses a non-root user, and has no package downloads.

The ordinary mode exercises startup, pod recovery, image replacement, and
cleanup. Qualification supplies direct command arguments for intentional faults:

| Argument | Actual behavior |
| --- | --- |
| `never` | Process stays alive without writing a heartbeat. |
| `stale` | Keeps a positive TTL but writes a timestamp two minutes old. |
| `frozen` | Refreshes TTL while retaining one timestamp. |
| `future` | Publishes a timestamp two minutes ahead: freshness cannot be established. |
| `malformed` | Writes a value that is not valid JSON: observation must remain an error. |
| `exit` | Exits with code 23 before publishing. |
| `first-pod-only` | A Redis `SET NX` claim allows only the first pod to publish. Later pods stay alive without publishing; the old heartbeat expires naturally. |
| `fail-second-start` | A bounded Redis startup counter suppresses only the second process. Restoration and image B publish again, proving a valid failure remains visible after continuation. |

The ownership marker is a separate fixture-only Redis key with a one-hour TTL,
longer than the qualification's 40-minute verification and 12-minute cleanup observation
bounds. The application heartbeat's TTL remains three seconds.
CloudForge must not treat it, or the predecessor's residual heartbeat, as proof
that a replacement is healthy. No key deletion or repository-specific behavior
is added to CloudForge for these tests.

These observations prove bounded process/heartbeat lifecycle requirements. They
do not prove queue job completion, exactly-once delivery, retries, or business
processing correctness. Runtime qualification runs only on disposable hosted
runners; the harness defaults to read-only inspection.
