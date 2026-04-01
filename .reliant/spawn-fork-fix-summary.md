# Spawn Fork Context Inheritance Bug Fix

## Summary
Fixed critical bug where spawned child workflows didn't inherit parent conversation context. The root cause was that `ForkFromThread` used `ordinal=0` which filtered out all parent messages (since message ordinals start at 1).

## Root Cause
**File**: `internal/threads/service.go:270`
```go
forkAtOrdinal: 0, // Comment said "Include all parent messages" but ordinal 0 actually excludes everything!
```

**File**: `internal/threads/messages.go:98`
```go
if msg.Ordinal <= forkOrdinal {  // ordinal <= 0 is ALWAYS FALSE (messages start at ordinal 1)
```

## The Fix

### 1. Changed ForkFromThread to calculate maxOrdinal dynamically
**File**: `internal/threads/service.go`
- Now calls `GetNextOrdinal()` to get the next available ordinal
- Calculates `maxOrdinal = nextOrdinal - 1` to include all existing messages
- Empty parent threads correctly get `maxOrdinal = -1` (no messages to inherit)

### 2. Removed special-case ordinal=0 handling
**File**: `internal/threads/messages.go`
- Removed the hacky `forkOrdinal == 0 ||` check
- Now uses standard `msg.Ordinal <= forkOrdinal` filtering

### 3. Removed DEBUG-REPRO logging
Cleaned up temporary debug logging from:
- `internal/threads/service.go`
- `internal/workflow/v2/workflow.go`
- `internal/workflow/v2/activities/handlers/workflow_status.go`

## Tests Added

### Regression Tests (internal/threads/spawn_fork_test.go)

1. **TestSpawnFork_E2E_ChildInheritsParentMessages** (already existed, now passes)
   - Full spawn workflow flow
   - Verifies child thread gets all parent messages

2. **TestSpawnFork_ForkFromMessage_IncludesMessagesUpToPoint** (NEW)
   - Fork from specific message at ordinal 3
   - Verifies only messages 1-3 are inherited

3. **TestSpawnFork_NestedForks_InheritThroughChain** (NEW)
   - Parent A → Child B → Grandchild C
   - Verifies C sees A's messages through the chain

4. **TestSpawnFork_TokenCount_InheritedWithOrdinalZero** (NEW)
   - Tests token counting inheritance
   - Verifies child inherits parent's token count correctly

### Updated Test Expectations
Several existing tests expected `ForkAtOrdinal=0` but now correctly expect the calculated maxOrdinal:
- `TestCreateWorkflowWithThread_ForkFromThread_SetsForkMetadata`: expects ordinal=1 (parent has 1 msg)
- `TestLoadCurrentMessages_ForkedThread_ReturnsParentMessages`: uses ordinal=2
- `TestCreateWorkflowWithThread`: expects ordinal=-1 (empty parent)

## Verification

All test suites pass:
```bash
go test ./internal/threads/... -v    # ✅ All pass
go test ./internal/db/... -run "Token" -v  # ✅ All pass
```

## What This Fixes

Before this fix:
- Spawned workflows saw ZERO parent messages
- Context was lost across workflow boundaries
- Spawn tool was essentially broken for conversation continuity

After this fix:
- Spawned workflows correctly inherit ALL parent messages
- Full conversation context maintained
- Token counting inheritance works correctly
- Nested spawns (A→B→C) properly chain context

## Files Changed

1. `internal/threads/service.go` - Fixed ForkFromThread to calculate maxOrdinal
2. `internal/threads/messages.go` - Removed ordinal=0 special case
3. `internal/db/repository_impl.go` - Removed ordinal=0 workaround in token counting
4. `internal/threads/spawn_fork_test.go` - Added comprehensive regression tests
5. `internal/threads/service_test.go` - Updated test expectations
6. `internal/workflow/v2/*.go` - Removed DEBUG-REPRO logging

## No Backwards Compatibility Needed

Per project guidelines: "we haven't launched. Thus we never require backwards compatibility, and all changes should result in removing old code paths."

This fix removes the broken ordinal=0 approach entirely.
