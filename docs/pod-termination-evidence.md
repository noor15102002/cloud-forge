# Safe container termination evidence

HTTP startup/readiness evidence and worker startup, recovery, and image replacement evidence can include bounded termination diagnostics from the existing Kubernetes pod observations. These diagnostics add no runtime commands and do not change an experiment's verdict.

Only the container named `application` is eligible. CloudForge retains two separate Kubernetes status slots:

- **Current:** `state.terminated`, when the observed container is terminated.
- **Previous:** `lastState.terminated`, including when its replacement instance is running or waiting in `CrashLoopBackOff`.

Both slots can exist in the same snapshot. They are not interchangeable, and neither is a history of every restart. Other containers, init containers, logs, termination messages, environment values, and custom reason text are excluded.

Each slot contains a fixed reason classification (`oom_killed`, `completed`, `error`, `cannot_run`, `start_error`, `deadline_exceeded`, or `unknown`), an exit code from 0 through 255, and a signal from 0 through 64. Missing, null, or out-of-range numbers are explicitly unknown. A missing or ambiguous application-container status is also explicitly unknown; it is not interpreted as a successful exit.

Reports expose sorted `pod_termination_*` aggregate measurements. For example, `pod_termination_previous_reason_oom_killed: 2` means two pods in that snapshot have a previous application-container termination classified by Kubernetes as `OOMKilled`. `pod_termination_current_exit_code_1: 1` means one pod has a current application-container termination with exit code 1. These are counts of status slots in pods, not cumulative event totals. Exit code 137 alone does not establish an out-of-memory cause.

`pod_termination_snapshot_scope` is `latest_successful_pod_observation_not_event_totals`. An observed empty pod list records zero pods and zero slots. If no usable observation exists, no termination measurements are produced. Worker evidence retains its latest successful observation when a later observation fails, and a later successful empty observation replaces an older populated snapshot. A retained snapshot does not establish the final state after an observation error. Restoring the worker does not rewrite the experiment's original failure or its recorded snapshot.

These fields describe only what Kubernetes reported. They do not infer whether the application has a defect, a test configuration is unsuitable, or more memory would resolve a failure. Historical reports that lacked termination diagnostics remain unchanged; missing evidence cannot be reconstructed from restart counts.
