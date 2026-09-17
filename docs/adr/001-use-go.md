# ADR 001: Use Go for CloudForge

Status: Accepted

CloudForge uses Go 1.27. Go provides portable binaries, strong process and
context primitives, and mature Kubernetes libraries. The module declares
language compatibility as `go 1.27` and suggests `toolchain go1.27.1` without
binding compatibility to a patch release.

