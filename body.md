## review:passed

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: prod-deploy (deploy/kcl/config_gen.k)

Reviewed every changed hunk and generated-file inputs. All four previous blockers are fixed. The merged command/config wiring preserves preview alongside worktree. TestMachineLifecycleAndPreviewAccess covers request construction, destructive-action confirmation, overflow, unsupported snapshot/token modes and agent-shell denials; TestPreviewLoadsCLIConfigThroughKCL covers config projection and missing-config refusal. No new blocking defect found in this push. RC-08/10/11 checked; no dependency-pin, billing-calculation, migration or retained-name changes are introduced by this diff.

**Second pass:** All supplied changed hunks reviewed. Earlier blockers are fixed. The additional server source settles the remaining authorization/scoping coverage gap. Shared KCL projection and CLI refusal tests cover the changed behavior; rollout checks and rollback are documented. No PR code or tests were executed.

Tests covering the risk: 
- Production-shared KCL configuration projection and missing-config refusal: `cmd/reliant/cmd/cli_config_preview_kcl_test.go:TestPreviewLoadsCLIConfigThroughKCL`
- Lifecycle request bounds, destructive-operation confirmation, and agent-shell access-write denial: `cmd/reliant/cmd/machine_lifecycle_test.go:TestMachineLifecycleAndPreviewAccess`

Migration safety: **not_applicable**

**Earlier blockers, at this head:**
- R1.B1 fixed — cmd/reliant/cmd/preview_access.go:159 rejects token mode before SetPortAccess; machine_lifecycle_test.go tests that refusal.
- R1.B2 fixed — cmd/reliant/cmd/machine_lifecycle.go:127 parses at 32-bit width before conversion; machine_lifecycle_test.go covers the overflowing input.
- R2.B1 fixed — cmd/reliant/cmd/machine_lifecycle.go:82 refuses --snapshot before client discovery or deletion; machine_lifecycle_test.go verifies no extra mutation.
- R3.B1 fixed — The current PR body contains Post-merge check and Rollback sections covering disposable-machine lifecycle, port rules and released-CLI configuration.
- R1.B1 fixed — cmd/reliant/cmd/preview_access.go rejects token writes; TestMachineLifecycleAndPreviewAccess asserts rejection before RPC.
- R1.B2 fixed — cmd/reliant/cmd/machine_lifecycle.go parses disk size at int32 width; TestMachineLifecycleAndPreviewAccess asserts overflow rejection.
- R2.B1 fixed — cmd/reliant/cmd/machine_lifecycle.go refuses --snapshot before constructing a client; the lifecycle test asserts no delete RPC for it.
- R3.B1 fixed — PR body contains Post-merge check and Rollback sections covering released CLI effects and persisted resources.

**Generated, reviewed through their inputs (not line by line):** `cmd/reliant/cmd/roots_gen.go`, `frontends/web/src/gen/reliant/config/v1/cli_config_pb.ts`, `gen/reliant/config/v1/cli_config.pb.go`, `generated/docs-source/reference/cli.md`, `internal/app/graph_gen.go`, `pkg/config/config_gen.go`

**Not verified (non-blocking):**
- high_risk: Full server authorization/scoping implementations for CreateDaemon and port-access RPCs were not supplied: control-plane:internal/svcdaemon/service.go@e829e98837b9 outside the provided window. Supplied proto and CLI-identity test establish only part of that contract.

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- Released CLI lifecycle operations against production on a disposable machine. — Use the PR's drain-check procedure to create, grow and delete a throwaway machine; verify Settings and machine list; confirm snapshot refusal.; rollback: Revert and release; remove any test machine through Settings. Deletion is explicitly irreversible and restricted to a throwaway machine.
- Persisted preview access rules and released-CLI connection. — Use the PR's set/list/remove/default procedure and compare with Settings; verify released preview access list dials the configured control plane.; rollback: Revert and release; clean up test rules using the app's port-access controls.
- Released CLI machine lifecycle operations against production on a disposable machine — PR body: create drain-check, verify Settings and machine list, grow its disk, then delete with --yes; confirm --snapshot refusal.; rollback: PR body: revert and release; remove disposable machines through Settings. Server RPCs remain unchanged.
- Persisted preview access rules and released CLI configuration — PR body: set a disposable port public, list, remove, and set default authenticated; compare Settings and verify the configured control-plane connection.; rollback: PR body: revert and release; restore or remove persisted port rules through the app's port-access controls.

Passed: entering the native merge queue at this exact head.

Reviewed `56063a495a7efcd8858aee9a2c10837dba1567eb`.
<!-- review-passed sha=56063a495a7efcd8858aee9a2c10837dba1567eb -->