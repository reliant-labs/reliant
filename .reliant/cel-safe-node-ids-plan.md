# CEL-safe node IDs plan

## User direction
- Do **not** normalize invalid node IDs.
- Enforce CEL-safe node IDs and **fail validation** when IDs are invalid.
- No backwards compatibility required.
- Migrate existing workflows/tests/fixtures to the new contract.

## Chosen contract
Node IDs must be valid CEL-safe identifiers:
- start with a letter
- remaining chars: letters, digits, underscores
- **hyphens are not allowed**

Example valid IDs:
- `agent_loop`
- `router_1775339690239`
- `call_llm_1`

Example invalid IDs:
- `router-1775339690239`
- `save-msg-node`
- `1router`

## Why this path
This removes the language mismatch at the root.

Current user syntax is:
- `nodes.some_node.response_text`

That only works cleanly if `some_node` is a CEL identifier. Hyphenated IDs require lowering/translation. We already improved validator lowering, but the simpler permanent solution is to stop allowing non-CEL-safe IDs.

## Current state before this pivot
### Completed already
1. Investigated the stale `agent_loop` validation issue.
2. Patched frontend save-time stale-ref cleanup in:
   - `web/src/components/workflow/WorkflowBuilder.tsx`
   - `web/src/components/workflow/workflowRef.ts`
3. Reproduced real CLI validation on `pure-hawk-17f6`.
4. Fixed validator-side handling for hyphenated IDs by introducing encoded validator identifiers and a source-aware lowering pass in:
   - `internal/workflow/validation/cel.go`
   - `internal/workflow/validation/cel_env.go`
5. Added backend regression tests in:
   - `internal/workflow/validation/cel_compilation_test.go`
6. Verified:
   - targeted Go tests passed
   - `go run ./cmd/reliant workflow validate /tmp/pure-hawk-17f6.yaml` passed with warnings only
   - frontend focused tests passed

### Important note
Those validator-lowering improvements are now a transitional safety net, not the final product direction.

## New plan from here
### 1. Enforce CEL-safe node ID validation everywhere
Update validation rules so node IDs only allow:
- `^[A-Za-z][A-Za-z0-9_]*$`

Key places identified:
- backend structural validation:
  - `internal/workflow/validation/structural.go`
- frontend node ID validation UI:
  - `web/src/components/workflow/config/ConfigPanel.tsx`

Also update any user-facing validation/help text that currently says hyphens are allowed.

### 2. Stop generating hyphenated node IDs in the builder
Trace and update builder-generated IDs / helper naming paths.

Known relevant areas:
- `web/src/components/workflow/WorkflowBuilder.tsx`
- possibly flow/layout/helper code generating ids like:
  - `router-...`
  - `run-node`
  - `workflow-node`
  - `approval-node`
  - `save-msg-node`

Goal:
- new generated IDs use underscores, not hyphens
- no silent normalization of user-edited invalid IDs; validation should fail instead

### 3. Migrate existing workflows, fixtures, and tests
Search results already show many hyphenated node-id usages across:
- builtin workflows
- examples
- runtime test fixtures
- frontend tests
- validation tests

Representative files likely needing migration:
- `internal/workflow/builtin/*.yaml`
- `examples/workflows/*.yaml`
- `examples/scenarios/**/*.yaml`
- `internal/workflow/builtin/scenarios/**/*.yaml`
- `internal/workflow/builtin/testdata/*.yaml`
- `internal/workflow/runtime/testdata/**/*.yaml`
- `web/src/lib/__tests__/workflow-serializer-roundtrip.test.ts`
- `web/src/components/workflow/__tests__/*.ts?(x)`
- `internal/workflow/validation/cel_compilation_test.go`

Migration rule:
- rename hyphenated node IDs to underscore equivalents
- update all references in:
  - `entry`
  - `edges`
  - `outputs`
  - node-scoped CEL
  - save_message content/templates
  - scenario fixtures / assertions

### 4. Keep validator lowering for now, but it should become irrelevant
After migration, normal authored workflows should not rely on hyphenated node IDs.
The current source-aware lowering can remain temporarily, but the intended steady state is that it no longer needs to rescue invalid IDs in normal workflows.

A follow-up cleanup can later remove or shrink the lowering path once migrated coverage is stable.

## Concrete findings from the trace
### Frontend
- `WorkflowBuilder.tsx` still contains name/help text oriented around hyphens:
  - line near 330: comment says replace spaces with hyphens
  - line near 3011: UI text says “Use lowercase letters, numbers, and hyphens”
- `ConfigPanel.tsx` has `isValidNodeId(...)`
- tests contain many hyphenated ids, including:
  - `router-1775337214113`
  - `router-123`
  - `router-a`
  - `router-1`

### Backend
- `internal/workflow/validation/structural.go` currently allows hyphens via:
  - `^[a-zA-Z][a-zA-Z0-9_-]*$`
- error text also says hyphens are allowed
- validator compile path currently still supports encoded synthetic ids, but user requested final direction is CEL-safe ids only

## Recommended execution order
1. Tighten backend identifier regex and messages in `structural.go`
2. Tighten frontend `isValidNodeId(...)` and related UI copy in `ConfigPanel.tsx`
3. Update any builder-generated node-id factories to use underscores
4. Migrate builtin/example/test YAML and TS/Go fixtures
5. Update frontend tests expecting hyphenated IDs
6. Run targeted tests
7. Run broader CLI validation on builtin/example workflow directories

## Verification checklist
### Backend
- targeted validation tests
- any tests covering builtin workflow validation
- CLI validation over representative workflow sets

Suggested commands:
- `go test ./internal/workflow/validation`
- `go run ./cmd/reliant workflow validate internal/workflow/builtin --include-builtins`
- `go run ./cmd/reliant workflow validate examples/workflows`

### Frontend
Suggested commands:
- `cd web && npm test -- --run src/components/workflow/__tests__/workflowRef.test.ts`
- `cd web && npm test -- --run src/components/workflow/__tests__/NodeDetailsPanel.test.tsx`
- `cd web && npm test -- --run src/lib/__tests__/workflow-serializer-roundtrip.test.ts`
- plus any builder/config tests affected by node-id validation changes

## Risks / watchouts
- Do not silently mutate user-entered IDs at validation time; user explicitly asked to fail validation, not normalize.
- Be careful to update all CEL references when migrating ids in YAML/tests.
- Some scenario/test fixtures may indirectly reference node ids in logs/assertions.
- Existing validator lowering tests for hyphenated IDs may need to be reframed as transitional behavior or removed if the new contract forbids such IDs.

## Suggested next implementation task
Start with:
- `internal/workflow/validation/structural.go`
- `web/src/components/workflow/config/ConfigPanel.tsx`
- `web/src/components/workflow/WorkflowBuilder.tsx`

Then migrate fixtures/tests in a second pass.
