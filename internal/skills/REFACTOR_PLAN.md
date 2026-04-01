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

## Current-to-target mapping

### Current `types.go`
- shared primitives (`Scope`, `SkillFormat`, `Diagnostic`, `Notice`, `SupportingFile`) -> `core`
- compatibility aliases stay in `skills`
- lifecycle structs should eventually split into:
  - `catalog.Definition`
  - `materialize.ActiveSkill`

### Current `discovery.go`, `catalog_index.go`, `parser.go`, `validation.go`, `builtin_skill_creator.go`
- move under `catalog`
- preserve current exported `skills.DiscoverWithOptions`, `skills.DefaultCatalogIndex` via thin compatibility wrappers during migration

### Current `invocation.go`, `engine.go`
- explicit invocation parsing + turn classification + auto selection -> `activation`
- current `skills.ResolveActiveSkill` can remain as a compatibility facade while service migrates callers

### Current `strategy.go` + parts of `engine.go` + supporting-file helpers in `discovery.go`
- move under `materialize`
- integrate filesystem/tool shaping more explicitly around runtime materialization

### Current `policy.go`
- move to `policy`
- `skills.allowedToolsPolicyEngine` can become a thin wrapper or be retired once service owns orchestration

### Current `prompt.go`
- move to `prompt`
- call sites should eventually consume structured prompt output instead of raw mixing in `ResolveTurnResult`

### Current `runtime.go`
- replace with `service.Resolve`
- keep `skills.DefaultRuntime().ResolveTurn` as temporary compatibility shim until integrations and tests migrate

### Current `call_llm.go`
- consume `skills/service`
- eventually remove direct knowledge of fragmented runtime internals

## Compatibility strategy

1. Introduce new subpackages with narrow types/tests.
2. Re-export or alias shared primitives from `skills` to avoid broad breakage.
3. Add `service.Resolve` as the new integration seam while delegating to current runtime initially.
4. Gradually migrate internal implementations into subpackages, then simplify `runtime.go` down to a shim or remove it.
