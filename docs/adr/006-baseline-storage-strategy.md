# ADR 006: Use Explicit Versioned JSON Baselines

Status: Accepted for V1

The regression engine will accept an explicit previous verification JSON file.
The comparison engine will not depend directly on GitHub artifact APIs. A
workflow may download a selected default-branch artifact and pass it as the
baseline, keeping authentication and run selection outside the core engine.

JSON schemas are versioned. Collections with semantic order are explicitly
sorted; consumers must never rely on object key order.
