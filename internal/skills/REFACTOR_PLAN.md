# Skills pipeline refactor map

This document maps the current flat `internal/skills` package into the target pipeline-shaped architecture.

## Target layers

1. `catalog`
   - discovery roots and precedence
   - parsing/validation
   - caching/index snapshots
2. `activation`
   - explicit invocation parsing
   - intent classification
   - auto-selection heuristics
3. `materialize`
   - full definition loading
   - supporting-file collection
   - retrieval/chunk selection
4. `policy`
   - allowed-tools filtering
   - future trust/tool policy
5. `prompt`
   - available-skills rendering
   - active-skill rendering
   - notices/trust-boundary sections
6. `service`
   - orchestration boundary consumed by integrations

## Progress

### Subpackages created
- [x] `core` — shared primitives (`Scope`, `SkillFormat`, `Diagnostic`, `Notice`, `SupportingFile`)
- [x] `catalog` — discovery, catalog index, parsing, validation, builtin skill creation
- [x] `activation` — explicit invocation parsing, turn classification, auto selection
- [x] `materialize` — full definition loading, supporting-file collection, runtime materialization
- [x] `prompt` — available-skills rendering, active-skill rendering
- [x] `service` — orchestration boundary

### Remaining in root package (not yet migrated)
- [ ] `policy` — `policy.go` still lives in root `skills/`; move to `policy/` subpackage
- [ ] `engine.go` — may contain logic that should be split across `activation` and `materialize`
- [ ] `invocation.go` — may overlap with `activation` subpackage
- [ ] `runtime.go` — replace with `service.Resolve`; remove once integrations and tests migrate
- [ ] `types.go` — remaining type definitions that haven't moved to `core`

## Current-to-target mapping

### `types.go`
- Shared primitives (`Scope`, `SkillFormat`, `Diagnostic`, `Notice`, `SupportingFile`) → `core`
- Lifecycle structs should split into:
  - `catalog.Definition`
  - `materialize.ActiveSkill`

### `discovery.go`, `catalog_index.go`, `parser.go`, `validation.go`, `builtin_skill_creator.go`
- Move under `catalog`

### `invocation.go`, `engine.go`
- Explicit invocation parsing + turn classification + auto selection → `activation`

### `strategy.go` + parts of `engine.go` + supporting-file helpers in `discovery.go`
- Move under `materialize`
- Integrate filesystem/tool shaping more explicitly around runtime materialization

### `policy.go`
- Move to `policy` subpackage
- `skills.allowedToolsPolicyEngine` can be retired once service owns orchestration

### `prompt.go`
- Move to `prompt`
- Call sites should consume structured prompt output instead of raw mixing in `ResolveTurnResult`

### `runtime.go`
- Replace with `service.Resolve`
- Remove once integrations and tests migrate

### `call_llm.go`
- Consume `skills/service`
- Remove direct knowledge of fragmented runtime internals
