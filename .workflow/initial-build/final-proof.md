# Final composed Linux proof

This is a read-only factory planning and QA packet for the integrated Orchard app and CLI. Do not change product code or run Apple account operations. The coordinator will select the final composed source revision before QA. No Linear binding or release is authorized.

Planning declares `workScope` exactly `runtime-interactive` and exactly one proof target: `{"platform":"web","flow":"orchard-workspace","requiredMedia":["image","video"]}`. The proof covers the local graphical workspace and CLI. It does not establish an iPhone/iPad run, production signing, TestFlight, upload processing, or review submission.

Acceptance criteria:

- The selected source passes Go tests with the race detector, vet, and Linux arm64/amd64 compilation using the configured Linux runner.
- The amd64 CLI executes version, project creation and prerequisite plans in pinned Arch Linux under CPU emulation. Native Omarchy desktop support is a separate unproved environment.
- Node tests, actual GIO interpretation of the installed desktop entry, and container private-state contracts pass as a non-root Linux user.
- A fresh non-root graphical session installs the actual pinned ASC binary before a project exists, verifies its hash/version, and visibly reports the setup outcome.
- The graphical session configures an explicitly synthetic signing identity, keeps private fixture paths out of public plans and results, selects that identity for export, and reports missing bundle/tool prerequisites without enabling execution. No Apple identity is used.
- The live browser creates a project, displays truthful missing build prerequisites, invalidates stale plans, refuses unauthenticated and foreign-Origin API access, and fits a narrow viewport without page errors.
- Retained screenshots, video, test results and source/binary hashes identify the exact tested revision.

All compilation and tests run inside Linux. The trusted host command only orchestrates Linux containers and collects evidence. Request only the allowlisted command ID `orchard-linux-proof`, platform `web`, flow `orchard-workspace`. Do not invoke browser automation, compilers or tests on macOS, and do not invent evidence before the trusted runner returns.

Return exactly one strict `<factory-result>` JSON report. Planning uses proofPolicy `none`, artifacts `[]`, proofRequests `[]`, nonempty proofReason, and no review findings. QA uses proofPolicy `video`, artifacts `[]` and proofRequests `[{"commandId":"orchard-linux-proof","platform":"web","flow":"orchard-workspace"}]`; the factory collects validated artifacts from the trusted runner. Every check includes name, status, summary and required/acceptance flags. Include a nonempty summary and acceptanceCriteria. No null fields or unsupported workScope values.
