<!-- cloudforge-verification-report:v1alpha1 -->
## CloudForge verification

**Status:** WARN · **Application:** checkout\|api &lt;script&gt; · **Duration:** 1250 ms

**Environment:** k3d / cloudforge-run-123 / cloudforge · **Endpoint:** http://127.0.0.1:18080/ready

| Experiment | Status | Duration | Result |
|---|---:|---:|---|
| Container build | **PASS** | 400 ms | Image built. |
| Horizontal autoscaling | **SKIPPED** | 0 ms | No HPA configured. |
| Bounded load | **WARN** | 800 ms | Latency crossed \| target |

<details>
<summary>Measurements (2)</summary>

| Experiment | Measurement | Value |
|---|---|---:|
| load-profile | latency&#95;p95&#95;ms | 120.000 ms |
| load-profile | request&#95;count | 200 requests |

</details>

<details>
<summary>Findings (2)</summary>

### container.startup · PASS/INFO

Application started.

- **Category:** container
- **Observed:** 2/2 ready
- **Expected:** all replicas ready
- **Duration:** 400 ms

### runtime.load-profile · WARN/MEDIUM

Latency exceeded the expected threshold. Investigate.

- **Category:** reliability
- **Observed:** 120 ms
- **Expected:** below 100 ms
- **Duration:** 800 ms
- **Source:** deploy\|app.yaml document 2 field spec.template
- **Remediation:** Tune &#96;worker&#96; count.

</details>

### Diagnostics

- **WARN · hpa&#95;metrics&#95;unavailable:** CPU metrics unavailable. Check metrics-server. Source: deploy\|app.yaml document 2.

<sub>Schema v1alpha1 · Run run-123</sub>
