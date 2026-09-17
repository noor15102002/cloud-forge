# ADR 005: Build Local Execution First

Status: Accepted

The first executor will run disposable k3d clusters locally and in Ubuntu CI.
Cloud executors remain possible through a later boundary, but CloudForge V1
will not provision Azure, Terraform, or hosted infrastructure.

