<!-- cloudforge-verification-report:v1alpha1 -->
## CloudForge verification

**Status:** WARN · **Application:** checkout&#124;api &lt;script&gt; · **Duration:** 1250 ms

**Environment:** k3d / cloudforge-run-123 / cloudforge · **Endpoint:** http&#58;//127&#46;0&#46;0&#46;1&#58;18080/ready

### Completed evidence

| Experiment | Status | Duration | Result |
|---|---:|---:|---|
| Container build | **PASS** | 400 ms | Image built&#46; |
| Horizontal autoscaling | **SKIPPED** | 0 ms | No HPA configured&#46; |
| Bounded load | **WARN** | 800 ms | Latency crossed &#124; target |

<details>
<summary>Measurements (2)</summary>

| Experiment | Measurement | Value |
|---|---|---:|
| load-profile | latency&#95;p95&#95;ms | 120&#46;000 ms |
| load-profile | request&#95;count | 200 requests |

</details>

### Baseline comparison

**Status:** FAIL · **Baseline run:** baseline-100 · **Regressions:** 1 · **Improvements:** 1 · **Unavailable:** 1

#### Regressions

| Evidence | Baseline | Current | Change |
|---|---:|---:|---|
| load-profile/latency&#95;p95&#95;ms | 90&#46;000 ms | 120&#46;000 ms | latency&#95;p95&#95;ms changed from 90&#46;000 ms to 120&#46;000 ms |

#### Improvements

| Evidence | Baseline | Current | Change |
|---|---:|---:|---|
| load-profile/throughput&#95;rps | 40&#46;000 requests/second | 50&#46;000 requests/second | throughput&#95;rps changed from 40&#46;000 requests/second to 50&#46;000 requests/second |

<details>
<summary>Unavailable comparisons (showing 1 of 1)</summary>

| Evidence | Reason |
|---|---|
| horizontal-autoscaling status | status comparison is unavailable for baseline pass and current skipped |

</details>

<details>
<summary>Findings (2)</summary>

### container&#46;startup · PASS/INFO

Application started&#46;

- **Category:** container
- **Observed:** 2/2 ready
- **Expected:** all replicas ready
- **Duration:** 400 ms

### runtime&#46;load-profile · WARN/MEDIUM

Latency exceeded the expected threshold&#46; Investigate&#46;

- **Category:** reliability
- **Observed:** 120 ms
- **Expected:** below 100 ms
- **Duration:** 800 ms
- **Source:** deploy&#124;app&#46;yaml document 2 field spec&#46;template
- **Remediation:** Tune &#96;worker&#96; count&#46;

</details>

### Diagnostics

- **WARN · hpa&#95;metrics&#95;unavailable:** CPU metrics unavailable&#46; Check metrics-server&#46; Source: deploy&#124;app&#46;yaml document 2.

<sub>Schema v1alpha1 · Run run-123</sub>
