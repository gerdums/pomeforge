# Orchard initial build

Build a Linux-first app and CLI for making iPhone and iPad applications without requiring the developer to own or access a Mac. Orchard is a working name.

## Success criteria

- An architecture grounded in current upstream tools, with explicit support and proof boundaries.
- A runnable local graphical workspace and machine-readable CLI using the same operations.
- Project creation, dependency diagnosis, inspectable build/device/store plans, guarded execution, and retained operation results.
- Tests, Linux build artifacts, and a documented path to physical-device and App Store verification.
- No silent Mac dependency, fabricated tool commands, credentials in source, or actual publication during development.

## Work packets

- Research: establish native Linux build, device, signing, upload, and submission feasibility from primary sources.
- Factory: inspect the installed software factory and configure isolated implementation and review work where supported.
- Core: project format, tool adapters, operation plans, execution, CLI, and tests.
- App: accessible responsive local graphical workspace using the core API.
- Integration: independent review, functional checks, Linux execution, documentation, and limitations.

## Policy

Keep packets disjoint and preserve other work. Follow the host software factory execution contract. No external publishing, signing changes, account mutations, or Linear Done transition. The developer does not need AI to use any feature; agents get structured commands and explicit action boundaries.

## Verification

Run meaningful core and HTTP security tests, build the distributable, exercise CLI flows in a disposable project, and inspect the graphical workspace. Report physical-device and App Store evidence separately from source and mocked adapter checks.
