# Migration: `provider` → `providers` (Array)

## Overview

Change `ModelSelector.Provider string` to `ModelSelector.Providers []string` across the codebase. This enables ordered provider preference/fallback instead of single hard constraint.

## ⚠️ CRITICAL NOTES FOR ALL AGENTS

1. **NO BACKWARDS COMPATIBILITY** - Remove old code paths entirely. Do not support both `provider` and `providers`. Just replace.
2. **PARALLEL EXECUTION** - Multiple agents will work on this simultaneously. Be careful of file conflicts.
3. **COORDINATE ON SHARED FILES** - If you see another agent working on a file you need, wait or coordinate.

## Semantic Change

| Before | After |
|--------|-------|
| `provider: "anthropic"` | `providers: ["anthropic"]` |
| Empty = system picks | Empty = system picks (unchanged) |
| Single value = hard constraint | Single value = hard constraint |
| N/A | Multi-value = ordered fallback |

---

## Work Streams (Parallelizable)

### Stream 1: Backend Core Types
**Owner:** Agent A
**Files:**
- `internal/llm/models/types.go`

**Changes:**
1. Change `ModelSelector` struct:
   ```go
   // BEFORE
   Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
   
   // AFTER
   Providers []string `yaml:"providers,omitempty" json:"providers,omitempty"`
   ```

2. Update all documentation comments referencing `Provider` field
3. Update `UnmarshalJSON` and `UnmarshalYAML` if they handle the Provider field

**Depends on:** Nothing
**Blocks:** Stream 2, Stream 3, Stream 5

---

### Stream 2: Backend Resolution Logic
**Owner:** Agent B
**Files:**
- `internal/llm/models/registry_v2.go`

**Changes:**
1. Update `Resolve()` function (line ~238-299):
   - Change `selector.Provider` references to `selector.Providers`
   - Update logic to iterate through providers array in order

2. Update `findBestProvider()` function (line ~339-380):
   ```go
   // BEFORE: func (r *ModelRegistry) findBestProvider(model *ModelDefinition, forcedProvider string, ...) 
   // AFTER:  func (r *ModelRegistry) findBestProvider(model *ModelDefinition, preferredProviders []string, ...)
   ```
   
   New logic:
   - If `len(preferredProviders) == 0`: use system priority (unchanged)
   - If `len(preferredProviders) == 1`: hard constraint (current behavior)
   - If `len(preferredProviders) > 1`: try each in order, first available wins

**Depends on:** Stream 1 (types must be updated first)
**Blocks:** Nothing (tests can be written against new interface)

---

### Stream 3: Backend Workflow Types
**Owner:** Agent C  
**Files:**
- `internal/workflow/v3/inputs.go`

**Changes:**
1. Update `ModelSelector` struct (line ~446-453):
   ```go
   // BEFORE
   Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
   
   // AFTER
   Providers []string `yaml:"providers,omitempty" json:"providers,omitempty"`
   ```

2. Update any validation logic that references Provider

**Depends on:** Nothing (separate type from models.ModelSelector)
**Blocks:** Stream 4, Stream 6

---

### Stream 4: Backend Tests
**Owner:** Agent D
**Files:**
- `internal/llm/models/registry_v2_test.go`
- `internal/workflow/v3/inputs_validate_value_test.go`
- `internal/workflow/v3/validation/inputs_test.go`
- `internal/workflow/builtin/workflows_test.go`

**Changes:**
1. `registry_v2_test.go`: Update all test cases that construct `ModelSelector` with `Provider` field
2. `inputs_test.go`: Update validation test helpers and test cases
3. Add new test cases for multi-provider fallback behavior:
   - Empty array = system default
   - Single element = hard constraint
   - Multiple elements = ordered preference
   - All preferred providers unavailable = error

**Depends on:** Stream 1, Stream 2, Stream 3
**Blocks:** Nothing

---

### Stream 5: Frontend Types & Components  
**Owner:** Agent E
**Files:**
- `web/src/types/workflow.ts` (if ModelSelector type exists)
- `web/src/components/workflow/WorkflowParamInput.tsx`
- `web/src/components/workflow/WorkflowParamsEditor.tsx`
- `web/src/components/Chat/ModelSelector.tsx`
- `web/src/lib/paramUtils.ts`
- `web/src/lib/__tests__/paramUtils.test.ts`

**Changes:**
1. Update any TypeScript interfaces that mirror `ModelSelector`
2. Update UI components that render/edit provider selection
3. Consider: Should the UI allow multi-select for providers? (Probably yes for advanced mode)
4. Update param utilities that handle model input defaults

**Depends on:** Stream 1 (need to know final field name)
**Blocks:** Nothing

---

### Stream 6: YAML Workflow Definitions
**Owner:** Agent F
**Files:**
- `internal/workflow/builtin/structured-agent.yaml`
- `internal/workflow/builtin/testdata/structured-agent_scenarios.yaml`
- Any other `.yaml` files with `provider:` in model contexts

**Changes:**
1. Search for any YAML using `provider:` under model/ModelSelector contexts
2. Update to `providers: [...]` array syntax
3. Most workflows probably don't specify provider (use defaults), so this may be minimal

**Depends on:** Stream 3 (workflow types)
**Blocks:** Nothing

---

### Stream 7: Protos (IF NEEDED)
**Owner:** Agent G (or skip if not needed)
**Files:**
- `proto/reliant/v1/catalog.proto`
- `proto/reliant/v1/workflow.proto` (if exists)

**Assessment:**
- `catalog.proto` has `ModelInfo.provider` - this is for **display grouping**, NOT selection. Probably unchanged.
- Check if there's a `ModelSelector` message in any proto
- If protos change, regenerate with `make proto`

**Likely outcome:** No proto changes needed. The `provider` field in `ModelInfo` is display metadata, not selection.

**Depends on:** Nothing (assessment first)
**Blocks:** Nothing

---

### Stream 8: Config Panel UI
**Owner:** Agent H
**Files:**
- `web/src/components/settings/*` (provider configuration)
- `web/src/components/workflow/WorkflowHub.tsx`

**Changes:**
1. If there's UI for configuring model defaults with provider preference, update it
2. WorkflowHub.tsx uses `ModelSelector` component - ensure it handles array properly

**Depends on:** Stream 5 (frontend types)
**Blocks:** Nothing

---

## Execution Order (Critical Path)

```
Stream 1 (Core Types) ──┬──► Stream 2 (Resolution Logic) ──► Stream 4 (Tests)
                        │
                        └──► Stream 5 (Frontend Types)  ──► Stream 8 (Config UI)

Stream 3 (Workflow Types) ──┬──► Stream 4 (Tests)
                            │
                            └──► Stream 6 (YAML Workflows)

Stream 7 (Protos) ──► Assessment only, likely no changes
```

## Parallel Execution Groups

**Group 1 (Start Immediately - No Dependencies):**
- Stream 1: Backend Core Types
- Stream 3: Backend Workflow Types  
- Stream 7: Proto Assessment

**Group 2 (After Group 1):**
- Stream 2: Resolution Logic (needs Stream 1)
- Stream 5: Frontend Types (needs Stream 1)
- Stream 6: YAML Workflows (needs Stream 3)

**Group 3 (After Group 2):**
- Stream 4: Tests (needs Stream 1, 2, 3)
- Stream 8: Config UI (needs Stream 5)

---

## Verification Checklist

After all streams complete:

- [ ] `go build ./...` passes
- [ ] `go test ./...` passes  
- [ ] `pnpm --filter web build` passes
- [ ] `pnpm --filter web test` passes
- [ ] Manual test: Create workflow with `providers: ["anthropic", "openrouter"]`
- [ ] Manual test: Verify single-provider still works as hard constraint
- [ ] Manual test: Verify empty providers uses system default

---

## Rollback

Not applicable - no backwards compatibility. If something breaks, fix forward.
