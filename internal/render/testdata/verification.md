<!-- cloudforge-verification-report:v1alpha1 -->
## CloudForge verification

**Status:** WARN · **Application:** checkout&#124;api &lt;script&gt; · **Duration:** 1250 ms

**Environment:** k3d / cloudforge-run-123 / cloudforge · **Endpoint:** http&#58;//127.0.0.1:18080/ready

### Completed evidence

| Experiment | Status | Duration | Result |
|---|---:|---:|---|
| Container build | **PASS** | 400 ms | Image built. |
| Horizontal autoscaling | **SKIPPED** | 0 ms | No HPA configured. |
| Bounded load | **WARN** | 800 ms | Latency crossed &#124; target |

<details>
<summary>Measurements (2)</summary>

| Experiment | Measurement | Value |
|---|---|---:|
| load-profile | latency&#95;p95&#95;ms | 120.000 ms |
| load-profile | request&#95;count | 200 requests |

</details>

### Baseline comparison

**Status:** FAIL · **Baseline run:** baseline-100 · **Status regressions:** 0 · **Status improvements:** 0 · **Advisory numerical changes:** 2 · **Unavailable:** 1

Numerical comparisons are experimental and advisory; fixed percentage thresholds do not establish statistical significance.

#### Advisory numerical comparisons

| Evidence | Baseline | Current | Change |
|---|---:|---:|---|
| load-profile/latency&#95;p95&#95;ms | 90.000 ms | 120.000 ms | latency&#95;p95&#95;ms changed from 90.000 ms to 120.000 ms |
| load-profile/throughput&#95;rps | 40.000 requests/second | 50.000 requests/second | throughput&#95;rps changed from 40.000 requests/second to 50.000 requests/second |

<details>
<summary>Unavailable comparisons (showing 1 of 1)</summary>

| Evidence | Reason |
|---|---|
| horizontal-autoscaling status | status comparison is unavailable for baseline pass and current skipped |

</details>

<details>
<summary>Findings (2)</summary>

### runtime.load-profile · WARN/MEDIUM

Latency exceeded the expected threshold. Investigate.

- **Category:** reliability
- **Observed:** 120 ms
- **Expected:** below 100 ms
- **Duration:** 800 ms
- **Source:** deploy&#124;app.yaml document 2 field spec.template
- **Remediation:** Tune &#96;worker&#96; count.

### container.startup · PASS/INFO

Application started.

- **Category:** container
- **Observed:** 2/2 ready
- **Expected:** all replicas ready
- **Duration:** 400 ms

</details>

### Diagnostics

- **WARN · hpa&#95;metrics&#95;unavailable:** CPU metrics unavailable. Check metrics-server. Source: deploy&#124;app.yaml document 2.

<sub>Schema v1alpha1 · Run run-123</sub>
