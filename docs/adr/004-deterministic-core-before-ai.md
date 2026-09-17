# ADR 004: Keep the Correctness Path Deterministic

Status: Accepted

Analysis uses structured metadata and verification will use measured runtime
behavior. An LLM will not determine whether requests failed, latency regressed,
pods recovered, or autoscaling occurred.

