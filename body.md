## review:passed

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: tenant-isolation (internal/db/repository_impl.go, internal/workflow/workflowlisttest/workflowlisttest.go)

Reviewed all changed hunks and generated-code inputs. R1.B1 is fixed: saved-scenario execution now checks workflow access before consuming its definition, with denial and authorized-path regression coverage. The extraction preserves tenant filters, live-membership checks, locking, transaction policy, version checks and transactional updates. No new blocker found. No billing, migration, secret or deployment-config change.

**Second pass:** Reviewed all supplied changed hunks and traced draft ownership filters, membership authorization, by-ID handler checks, and the saved-scenario fix at 7c9ba411651e. R1.B1 is fixed with denial and legitimate-access regression coverage. The extraction preserves scoped queries and transactional write checks. Applicable escaped-defect checks revealed no new violation. No schema, wire, price, secret, or deployment configuration change. Review was read-only; tests and gates were not executed.

Tests covering the risk: 
- Tenant isolation across extracted draft reads and slug-addressed writes: `internal/db/workflow_draft_tenant_test.go:TestWorkflowDrafts_AnotherUsersDraftsAreUnreachableByAnyKey`
- Live membership denial remains enforced by extracted scoped reads and writes: `internal/workflowdrafts/service_test.go:TestReads_AreScopedThroughAuthz; TestByIDWrites_DecideOnTheLockedRowThroughAuthz; TestUpsert_RefusesASlugWhoseRowTheCallerMayNotWrite; TestDelete_LeavesADraftTheCallerMayNotWrite`
- RC-02: retained scenarios cannot authorize access to associated workflows after membership removal: `internal/handlers/scenario/scenario_workflow_authz_test.go:TestRunScenarioChecksAssociatedWorkflowAccess`

Migration safety: **not_applicable**

**Earlier blockers, at this head:**
- R1.B1 fixed — internal/handlers/scenario/rpc_run_scenario.go:139 checks authz.CanReadWorkflow before accessing the definition; scenario_workflow_authz_test.go:25 covers missing drafts, foreign ownership, removed membership and authorized access.
- R1.B1 fixed — internal/handlers/scenario/rpc_run_scenario.go:137@7c9ba411651e checks CanReadWorkflow before consuming the definition; scenario_workflow_authz_test.go:25 covers missing drafts, foreign owners, removed members, current members and personal workflows.

**Generated, reviewed through their inputs (not line by line):** `gen/forge_service_catalog.json`, `internal/app/graph_gen.go`, `internal/handlers/preset/mock_gen.go`, `internal/handlers/scenario/mock_gen.go`, `internal/handlers/workflow/mock_gen.go`, `internal/llm/tools/mock_gen.go`, `internal/workflow/runtime/activities/handlers/mock_gen.go`, `internal/workflowdrafts/contract_gen.go`, `internal/workflowdrafts/middleware_gen.go`, `internal/workflowdrafts/mock_gen.go`

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- Workflow-builder operations, editor update delivery, stale-edit refusal, membership denial and service startup after rollout. — Perform the PR's save/rename/visibility/status/copy/import/fork/delete and two-tab checks; verify taken-slug and removed-member refusals and absence of Drafts dependency boot errors.; rollback: Revert this PR; no schema or wire change.
- Builder mutations and cross-tab update delivery — Exercise save, rename, hide/show, complete, copy, import, orphan fork and delete; verify the second tab updates once per write.; rollback: Revert this PR; no schema or wire change.
- Stale edits, slug conflicts and removed-member visibility — Exercise the PR's two-tab stale-edit check, taken-slug refusal, and removed-member reads by slug, name, list and run.; rollback: Revert this PR; no schema or wire change.
- Production dependency wiring — Check api-server, worker and gateway logs for Deps.Drafts initialization failures.; rollback: Revert this PR; no schema or wire change.

Passed: entering the native merge queue at this exact head.

Reviewed `7c9ba411651e71e673622b6b05e6d32a5cdd1bdf`.
<!-- review-passed sha=7c9ba411651e71e673622b6b05e6d32a5cdd1bdf -->