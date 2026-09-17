# ADR 002: Use k3d for Local Kubernetes

Status: Accepted for the next implementation phase

k3d will provide disposable, Docker-native Kubernetes environments suitable
for Linux, WSL2, CI, and approximately 16 GB development machines. CloudForge
will own cluster lifecycle and delete environments unless explicitly retained
for debugging.

