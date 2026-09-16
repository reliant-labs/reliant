# Tool Gating in Reliant — where the workflow author's `tools:` intent is ignored

Research only. Module `github.com/reliant-labs/reliant`, branch `engine`. All line
numbers verified against the working tree at time of writing.

---

## Q1. The escape hatch: `tools:` is advisory, not a boundary

**Answer: yes, `load_tool(name="write")` succeeds for a workflow that declared
`tools: [view, tag:readonly]`, as long as the workflow's `permission:` is
`mutating` or higher — which is the DEFAULT.**

### The trace

1. `internal/workflow/runtime/activities/handlers/call_llm.go:799-800` reads the
   node's `tools_config`. `toolsEnabled` is just "a filter is present".

2. `call_llm.go:802-806` resolves the permission independently of the filter:

   ```go
   permission := tools.PermissionMutating          // 803 — DEFAULT
   if tc != nil && model.CelStringIsSet(tc.GetPermission()) {
       permission = model.CelStringValue(tc.GetPermission())
   }
   ```

3. `call_llm.go:816-818` writes ONLY the permission into the global store:

   ```go
   if chat != nil {
       tools.GetLoadedToolsStore().SetPermission(chat.ID, permission)
   }
   ```

   **This is the drop point.** The filter (`tc.GetFilter()`) is never written to
   the store, and `LoadedToolsStore` has no field to hold it
   (`loaded_tools_store.go:15-21`: `tools`, `permissions`, `skills`,
   `availableMCP` — no `allowedFilter`).

4. The filter is used purely locally, to materialize this turn's tool list:
   `call_llm.go:993` (`toolFilter := model.CelStringListValue(tc.GetFilter())`)
   → `getAvailableToolsWithSpawn` (`call_llm.go:1743`) → `ExpandToolFilterWithSpawn`
   (`call_llm.go:1831`). The expanded set becomes `toolsList` and is then
   **discarded** when the activity returns. It is a per-turn projection, not a
   retained contract.

5. `load_tool.go:63-79` reads back only the permission
   (`GetLoadedToolsStore().GetPermission(chatID)`), and `loadTool`
   (`load_tool.go:81-128`) checks exactly one thing:

   ```go
   minPerm := MinimumPermissionForTool(name)          // 102
   if !PermissionAtLeast(permission, minPerm) { ... } // 103
   store.Add(chatID, name)                             // 117
   ```

   No filter is consulted because none was retained.

6. The newly-added tool is re-injected into the NEXT turn's tool list at
   `call_llm.go:1833-1842`, *after* filter expansion and unconditionally:

   ```go
   loadedTools := tools.GetLoadedToolsStore().Get(chat.ID)
   filterResult.ToolNames = append(filterResult.ToolNames, loadedTools...)
   ```

   The append is downstream of every filter rule, so even an explicit
   `!write` exclusion in the workflow's filter is overridden.

7. `load_tool` itself is force-granted to every tool-enabled agent regardless of
   filter (`call_llm.go:1897-1914`) — the comment asserts this "never escalates
   privileges" because each load is permission-gated. That is true of the
   *permission ladder* and false of the *workflow filter*, which is the gap.

8. The runtime enforcement point at execution
   (`execute_tools.go:218`, `:312-319`) re-checks the same permission ladder and
   nothing else, so the escape hatch survives to actual execution.

**Consequence:** `tools:` is a *starting set / prompt-surface* control, not an
access-control boundary. The real boundary is `permission:`, which is three
coarse levels, defaults to `mutating`, and (per `permissions.go:101-127`)
classifies the entire shell family as `readonly` — so even `permission: readonly`
hands the agent a shell that can `>` a file.

---

## Q2. How the permission level is chosen

- **Only caller of `SetPermission` in production code:** `call_llm.go:817`.
  (All other hits are tests.)
- **Source:** `tools_config.permission` on the `call_llm` node, a CEL string
  (`call_llm.go:804-805`).
- **Default when `tools:` is set but `permission:` is not:** `PermissionMutating`
  (`call_llm.go:803`). The workflow author gets write/edit-capable `load_tool`
  by omission.
- **Presets do NOT set permission.** A preset's `params` are merged into workflow
  *inputs* (`internal/preset/preset.go:654 ApplyToInputs`,
  `internal/workflow/runtime/inline_workflow_executor.go:240 loadAndMergePresets`,
  `:276 applyPresets`). Presets carry `tools:` and `spawn_presets:` (e.g.
  `builtin/presets/documentation.yaml`), which flow into `inputs.tools` —
  i.e. into the *advisory* filter, not the enforced level.
- **`builtin/agent.yaml:211-214`** is where the level actually comes from for
  the standard agent, and it is derived from `mode`, not from `tools`:

  ```yaml
  tools_config:
    filter: "{{inputs.mode == 'plan' ? ['tag:plan','tag:shell'] : inputs.tools}}"
    spawn:  "{{[spawn(workflow.name, inputs.spawn_presets)]}}"
    permission: "{{inputs.mode == 'plan' ? 'readonly' : 'mutating'}}"
  ```

  So a `code_reviewer` preset whose `tools:` list is deliberately read-only still
  runs at `mutating` whenever `mode != plan`, and can `load_tool("write")`.
- **Spawn cap:** `call_llm.go:807-815` lowers the child's permission to
  `rtx.ParentPermission` when the child asks for more. This is a genuine cap and
  it is the only place a parent constrains a child.

---

## Q3. The loaded-tools store — lifetime and scope (correctness)

`internal/llm/tools/loaded_tools_store.go`.

- **Process-global singleton:** `var globalLoadedToolsStore = &LoadedToolsStore{...}`
  (`:32-37`), returned by `GetLoadedToolsStore()` (`:40`). A single
  `sync.RWMutex`-guarded set of maps.
- **In-memory only.** Nothing serializes it; no DB table, no Temporal state.
- **Keyed by chatID alone** (`:17-20`). Not by run, not by thread, not by
  workflow, not by node.

The correctness consequences:

1. **`Clear()` is DEAD CODE in production.** `Clear` is defined at `:87` and the
   only callers anywhere in the tree are tests
   (`loaded_tools_store_test.go`, `load_tool_test.go`,
   `execute_tools_validation_test.go`). **Nothing ever clears a chat's loaded
   tools.** Entries accumulate for the process lifetime.

2. **Tools leak across runs of the same chat.** A tool loaded in run 1 is still
   in `store.Get(chatID)` in run 2, and `call_llm.go:1835-1837` appends it to the
   filter unconditionally. So a workflow that runs `mode: plan` *after* a
   previous `auto` run in the same chat inherits `write`/`edit` in its tool list.
   The permission gate at `execute_tools.go:312` would still reject a *mutating*
   tool for a readonly agent, but the schemas are offered and any tool at or
   below the current level passes.

3. **Tools leak across the spawn boundary.** A spawned child runs on the SAME
   `chatID` (`workflow.go:~2930`: `childExecContext.ChatID = chatID`) with only
   a new `Thread`. The store is not thread-keyed, so parent and child share one
   loaded set in both directions: a child's `load_tool` grants persist to the
   parent after the child returns.

4. **Permission is last-writer-wins per chat.** `SetPermission` is a bare map
   write on `chat.ID`. Concurrent/background spawns on one chat overwrite each
   other's level; `execute_tools.go:218` then reads whatever landed last. The
   parent-cap logic at `call_llm.go:809` is computed correctly but stored in a
   slot the child and parent share.

5. **A worker restart resets it.** Loaded tools vanish (agent silently loses a
   tool mid-chat), and so does the permission — `GetPermission` falls back to
   `PermissionOrchestrator` (`:148-157`, "backward compatible default"). That is
   fail-OPEN: between a restart and the next `call_llm`, any `execute_tools`
   read sees orchestrator.

---

## Q4. Every path by which a tool reaches an agent

| # | Path | Code | Who decides | Can the workflow author prevent it? |
|---|---|---|---|---|
| 1 | Workflow `tools:` filter | `call_llm.go:993`, `registry.go:185 ExpandToolFilterWithSpawn` | workflow author | Yes — this is the only one they own |
| 2 | Preset `params.tools` | `preset.go:654 ApplyToInputs`, `inline_workflow_executor.go:276` | preset author; overwrites `inputs.tools` | Partially — node args override preset params (`workflow.go:2567`) |
| 3 | `load_tool` (built-in) | `load_tool.go:81-128`, re-injected `call_llm.go:1833-1842` | the MODEL, gated only by permission | **No** |
| 4 | `load_tool` (MCP) | `load_tool.go:130-157` | the MODEL, gated only by "is it connected" | **No** |
| 5 | MCP tools via filter expansion | `call_llm.go:1809-1813`, `:1880-1885` | project MCP config + filter | Yes via filter, but see #4 |
| 6 | Forced `load_tool` grant | `call_llm.go:1897-1914` | hardcoded runtime | **No** — explicitly ignores the filter |
| 7 | Forced `spawn_send` grant | `call_llm.go:1916-1937` | hardcoded, when `mailboxReachable` | **No** (permission-gated at exec only) |
| 8 | Spawn tool from `spawn:` config | `call_llm.go:1023`, `:1957 getSpawnToolFromFilterConfig` | workflow `spawn_presets` | Yes (empty presets ⇒ `parseSpawnFilter` returns nil ⇒ disabled) |
| 9 | Response tool | `call_llm.go:1054` | runtime | No |
| 10 | `InitialToolsForPermission` | `permissions.go:38-73` | permission level | No — shell family is in `base` at EVERY level |
| 11 | Store leakage from a prior run / sibling thread | `call_llm.go:1835`, never cleared | nobody | **No** |
| 12 | Skills | `call_llm.go:1770 SetSkills`, consumed `local_executor.go:186` | project config | n/a (context, not tools) |

Note on #10: `InitialToolsForPermission` is the *discovery/advertisement* set
(used with `DeferredToolNames`, `loaded_tools_store.go:162`), and it includes
`shell`, `shell_list`, `shell_output`, `shell_wait`, `shell_kill` at readonly.

---

## Q5. Spawn / sub-agent inheritance

- Spawn config is parsed from `spawn:workflow(preset1,preset2)`
  (`registry.go:216 ParseSpawnEntry` / `:223 parseSpawnFilter`) or from
  `tools_config.spawn`; empty presets ⇒ spawn disabled.
- The tool itself is schema-only (`getSpawnTool`, `call_llm.go:1974`), built from
  preset descriptions loaded from stored project presets
  (`config.ParseStoredPresets`) then builtins (`builtin.BuiltinPresetsFS`).
- **The child's tools come from the CHILD's preset, not the parent's filter.**
  `buildSpawnChildInputs` (`workflow.go:2580-2598`) passes only `mode`,
  unattended propagation, and `parent_permission`. The parent's `tools:` list is
  not passed and does not constrain the child; the child's `inputs.tools` is
  whatever its own preset/workflow declares.
- **Can a readonly parent spawn a mutating child? No — on the permission axis.**
  `resolveParentPermission` (`workflow.go:2603`) derives `readonly` from
  `mode: plan` (or propagates an existing cap for chained spawns), it is set on
  `childExecContext.ParentPermission` (`workflow.go:~2940`), carried through
  `context.go:267/304` and `step_executor.go:1019`, and enforced at
  `call_llm.go:809-815`. That is the one working constraint in the system.
- **But the tool-set axis is uncapped**, and because the store is chat-keyed
  (Q3.3) the parent and child share loaded tools regardless.
- `maxSpawnDepth = 1` (`call_llm.go:795`) caps recursion.

---

## Q6. MCP bypasses the ladder entirely — confirmed

`load_tool.go:130-157`. The only check is availability:

```go
if !mcpToolAvailable(store.GetAvailableMCPTools(chatID), name) { ...error... }
// MCP tools are gated by MCP configuration, not the agent permission ladder.
store.Add(chatID, name)
```

There is no `MinimumPermissionForTool` call on this path. `SearchTools`
(`loaded_tools_store.go:225-236`) likewise reports every connected MCP tool as
`PermissionAllowed: true`. And `MinimumPermissionForTool` returns
`PermissionReadOnly` for unknown names (`permissions.go:97-98`), so even the
`execute_tools.go:312` re-check passes an MCP tool at every level.

**So: any agent — including a `permission: readonly`, `mode: plan` agent with
`tools: [view]` — can `load_tool` any connected MCP tool, including
mutating ones (`mcp__*__write_file`, browser automation, etc.).**

Scoping: `internal/config/config.go:28 MCPServer`, `:115
MCPServers map[string]MCPServer` — a project/user-level config map, resolved
per project path via `toolRuntime.EnsureProjectServersLoaded(ctx, scopePath)`
(`call_llm.go:1799`). Scope is therefore **per project**, not per workflow, per
agent, or per permission level. The workflow author has no say.

---

## Q7. Enforcement seams an airtight `tools:` contract would need

Naming choke points only; not designing.

1. **Store the intent.** `LoadedToolsStore` needs to retain the resolved allowed
   set (or the raw filter) alongside the permission — the one write at
   `call_llm.go:816-818` is where it would go.
2. **`loadTool`** — `load_tool.go:101-107`, add an allowed-set check beside the
   permission check.
3. **`loadMCPTool`** — `load_tool.go:146`, currently unguarded by anything but
   connectivity.
4. **The unconditional re-injection** — `call_llm.go:1833-1842`, where loaded
   tools are appended *after* filter expansion.
5. **The forced grants** — `call_llm.go:1897-1914` (`load_tool`) and
   `:1916-1937` (`spawn_send`), both of which bypass the filter by design.
6. **Execution-time gate** — `execute_tools.go:216-219` and `:312-319`, the last
   line of defense and currently permission-only. Must also see the allowed set,
   since only this seam catches a tool that entered by any other route.
7. **Store keying / lifecycle** — the key at `loaded_tools_store.go:17-20` must
   distinguish run and thread, and something must actually call `Clear`
   (`:87`, presently uncalled outside tests) at run boundaries.
8. **Fail-closed default** — `GetPermission`'s
   `return PermissionOrchestrator` fallback (`:156`) is fail-open on worker
   restart.
9. **Spawn child construction** — `workflow.go:2580-2598 buildSpawnChildInputs`,
   if a parent's tool set is ever to bound a child's the way `parent_permission`
   bounds its level.
10. **MCP scoping** — `config.go:115` / `call_llm.go:1799`, if MCP availability
    is to be narrowable below the project level.
