# Model-context inventory — task40e5b620

This inventory records the narrow memory-exclusion change, not an approved-only
request guarantee. Started from origin/main `22a9dd7a3`. No daemon deployment or
Growth AI activation is included. Source paths below are relative to Reliant.

## Request assembly and remaining sources

| Source | Exact source path / seam | Effect of memory exclusion |
|---|---|---|
| Automatic global/project memory and repo reliant.md snapshots | `internal/workflow/runtime/activities/handlers/call_llm.go`: `formatStoredMemories`, `streamLLMResponse`; `repo_memory.go`: `formatRepoMemoryMessages`; `internal/config/provider.go`: `GetProjectConfig`/stored config parsing | Omitted from assembled history, including repo-memory omission messages |
| Identity, parallel-agent/git safety rules, scratch-file advice | `call_llm.go`: `getSystemPrompts` | Retained |
| Working directory, bound worktree path, multi-repo names/relative paths | `call_llm.go`: `getSystemPrompts`; `chat_checkout.go`: `resolveCallCheckout`; `internal/projectcheckout/machine.go`: `ForMachine`; repository `ListReposByProject` | Retained when available; machine-less is not a metadata filter |
| Static capability/Forge routing guide | `internal/workflow/runtime/activities/handlers/reliant_capabilities.go`, `guidance/reliant_capabilities.md` | Retained |
| Skill names/descriptions | `call_llm.go`: `getSystemPrompts`; `internal/llm/tools/skill.go`: `SkillsAnnouncement` | Retained even with `skills: []`; empty config removes catalog only |
| Sandbox capability limitation note | `internal/workflow/runtime/activities/handlers/daemon_runtime_notes.go`: `daemonRuntimeLimitationNote` | Retained separately; historically attached inside memory formatter |
| Explicit system prompt, including preset-provided instructions | `call_llm.go`: `getSystemPrompts`, `args.GetSystemPrompt`; `internal/workflow/runtime/inline_workflow_executor.go`: `buildSubWorkflowInputsWithOwnership`/preset merge | Retained |
| Machine-less notes and GitHub repo hints | `internal/workflow/runtime/activities/handlers/no_machine.go`: `noMachineNotes`; `no_machine_repo.go`: repo-note helpers | Retained when applicable |
| Report-bug guidance | `internal/workflow/runtime/activities/handlers/bug_report_note.go`: `bugReportNote` | Retained when capability resolver says reachable |
| Preloaded skill bodies, explicit or preset-selected; Forge start-here | `call_llm.go`: `buildSeededSkillMessages`, `insertSeededMessagesAfterFirstUserTurn`; `preload_forge_skill.go`: `withForgeStartHere` | Retained if requested. Empty skills suppresses bodies, not catalog |
| Explicit per-call messages and thread injects | `call_llm.go`: `args.GetMessages`; `internal/workflow/runtime/child_workflow_init.go`: `initChildWorkflow`; `save_message.go` | Retained |
| History, images/files/attachments, summaries, assistant/tool exchanges | `call_llm.go`: `execute`, `loadConversationHistory`; `compact.go`; `internal/threads` | Retained. New thread avoids inherited history; fork/inherit do not. Exclusion never redacts old messages |
| User/sub-agent mailbox delivery | `call_llm.go`: `execute` before history load; `internal/workflow/runtime/activities/handlers/drain_agent_messages.go` | Retained |
| Workflow bootstrap/greenfield probe content | `internal/workflow/runtime/greenfield_probe.go`, `internal/workflow/runtime/activities/handlers/greenfield_probe.go` | Any already seeded messages remain; no automatic history purge |
| Tool names/descriptions/JSON schemas; dynamic grants, MCP/integration metadata, spawn preset descriptions | `call_llm.go`: `getAvailableTools`, `streamLLMResponse`; `internal/llm/tools/capabilities.go`; `call_llm.go` spawn-tool assembly; `internal/workflow/runtime/tool_capabilities.go` | Unchanged. Explicit empty preloaded AND loadable lists yields no tools |
| Router candidate workflow/preset metadata and prompts | `internal/workflow/runtime/router_executor.go`: `buildRoutingSystemPrompt`, `buildNodeRoutingSystemPrompt` | Retained; router requests inherit exclusion but are not context-free |
| Provider adapter additions | `internal/llm/drivers/anthropic/claude_code.go`: caller/base system blocks; `claude_prompts.go` and `claude_code_prompts_embed.go`; `internal/llm/drivers/antigravity/{driver,prompts,prompts_embed}.go`: managed system instruction; `internal/llm/drivers/openai/driver.go` and `internal/llm/drivers/codex/{driver,envelope_gpt56}.go`: developer/instruction/tool-envelope conversion and OpenAI-family guidance | Unchanged, independent of project memories |
| Provider transport/account fields | `internal/llm/drivers/*`: request serialization, model/account/session/working-directory options, media conversion | Not an exclusion surface. Verify selected provider's full wire request independently |

The actual send is `CallLLMActivity.streamLLMResponse` →
`driver.StreamResponse(systemPrompts, history, availableTools)` after
`prepareHistoryForLLM`. Regression tests capture this driver boundary and the
real OpenAI adapter's HTTP request body, not only memory-formatting helpers.
No claim is made about instructions a remote provider adds server-side.

## Existing minimal invocation: verified limits

Current tools API is `tools_config.preloaded_tools: []` plus
`loadable_tools: []`, not the removed `filter` spelling. An omitted loadable
list means all dynamically loadable tools, so it is not equivalent to empty.
`skills: []` prevents preload bodies but does not suppress `SkillsAnnouncement`.

A real handler request test uses durable `chats.no_machine = true`, an empty
project configuration, no tools and no skills. It still carries identity,
static capability guidance and machine-less notes. With a synthetic private
skill description in the config snapshot, that description still appears even
with the same empty tools/skills. This disproves an approved-only claim.
`internal/launch/launcher.go` rejects an empty ProjectID (`project_id is
required`); `research/NO_MACHINE_CHATS.md` explicitly defers project-less spaces.
No new project-less or bootstrap-suppression architecture was added.

## Policy durability

Approved follow-up: additive `threads.exclude_ambient_memory boolean NOT NULL
DEFAULT false`, migration `20261108064904_thread_ambient_memory_exclusion.up.sql`.
`internal/store/thread_repo_ext.go`: `ResolveThreadAmbientMemoryExclusion` reads
recursive parent lineage and monotonically latches true on the current thread.
`call_llm.go` does this before request assembly; an error blocks provider send.
`create_workflow_with_thread.go` latches at child initialization. Lineage must
reach an actual root within 256 threads: dangling parent/grandparent links,
cycles and depth truncation fail closed before provider send, even when the
surviving thread already has a true policy. Cross-chat fork ancestry is followed.
An already-true complete thread is not rewritten on each turn. Thus coarse
recovery and stopped-agent resume do not depend on their discarded runtime map.
Runtime context, thread tracker and continue-as-new handoffs also carry the flag.
No false/reset write exists. This does not retroactively establish exclusions
for legacy calls/threads before the field or flag existed; rolling back to an
old worker that ignores the field is not safe for the opt-out workflow.

## Full-provider-context checklist before Growth AI enablement

- [ ] Deploy a runtime that understands/enforces the flag and its migration;
      confirm every worker/provider path used by Growth runs that version.
      If this check fails, or anything regresses later, follow "If adoption
      fails or regresses" below. Never downgrade the worker while an opt-out
      run exists.
- [ ] Fresh non-forked history contains only approved brief/messages; inspect
      summaries, attachments, mailbox/report inputs and prior tool results.
- [ ] Approve or suppress all bootstrap bytes and project/repo/path metadata.
- [ ] Approve skill catalog names/descriptions, preload bodies and preset merges.
- [ ] Inspect complete tools array/schema/descriptions, loadable grants,
      integration/MCP/spawn metadata and all tool-returned data.
- [ ] Inspect selected provider's serialized request, adapter-added instructions,
      session/account/media metadata and any gateway transformation.
- [ ] Exercise nested workflows, routers, sequential/parallel loops, spawns,
      retries, continue-as-new, coarse recovery and stopped-agent resume.
- [ ] Use synthetic private canaries in every source; assert absent on the full
      outgoing request and approved context present. Do not test with real secrets.
- [ ] Independently review exact-head evidence. Keep Growth AI disabled until all
      applicable boundary checks are proved; memory exclusion alone is insufficient.

## If adoption fails or regresses

Rolling back the worker is not a recovery here. An old worker ignores
`threads.exclude_ambient_memory`, so it would send ambient memories for threads
that are durably excluded. Recovery stops the affected execution, keeps the
schema and the policy in place, and resumes only on a fixed runtime that knows
about exclusion.

**When the adoption check fails before Growth AI is enabled** (some worker or
provider path used by Growth does not run the exclusion-aware version): leave
Growth AI disabled and do not activate its workflows. Nothing has run, so
nothing needs pausing. Fix the deploy so every path runs the exclusion-aware
version, then repeat the check.

**When anything regresses after opted-out workflows have started** (a canary
shows up in an outgoing request, a worker on the wrong version takes a run, or
a release containing this change has to be withdrawn):

1. **Stop new work.** Deactivate every Growth workflow:
   `reliant trigger deactivate --workflow <name>`, one call per workflow.
2. **Stop the work in flight.** Find the affected runs: every chat whose thread
   lineage carries the opt-out: the threads with `exclude_ambient_memory =
   true` and every thread below them, including any descendant that has not
   yet run a turn to latch the flag itself. Pause names a **run** (a workflow
   execution id), not a chat, so the query returns each such chat's root run,
   `chats.workflow_id`, that has not finished. Query the reliant DB, read-only:

   ```sql
   WITH RECURSIVE opted AS (
     SELECT id, chat_id FROM threads WHERE exclude_ambient_memory
     UNION
     SELECT t.id, t.chat_id FROM threads t JOIN opted o ON t.parent_thread_id = o.id
   )
   SELECT DISTINCT c.workflow_id
   FROM opted o
   JOIN chats c ON c.id = o.chat_id
   JOIN workflows w ON w.id = c.workflow_id
   WHERE w.completed_at IS NULL;
   ```

   Pause each one with `reliant workflow pause <workflow_id>`. Pausing keeps
   the checkpoint; terminate (`reliant workflow terminate`) only a run that must
   never resume. Check each pause with `reliant workflow status <workflow_id>`,
   then run the query again: a run that was mid-turn can add children, and a
   new root may have started before step 1 took effect. Repeat until every
   root it returns is paused.
3. **Keep the schema and the policy.** Do not revert the migration, do not
   clear the column, and do not write `false` anywhere. The code has no reset
   path, and that is deliberate.
4. **Do not deploy an older worker** while any paused or running opt-out run
   exists. If the release must be withdrawn, fix forward: ship a release that
   still enforces the flag, and back out only the faulty change.
5. **Resume only on a verified runtime.** Before resuming any run, repeat the
   checklist's first check: every worker and provider path that Growth uses
   runs an exclusion-aware version, and a canary run of a nested, forked and
   continued-as-new opt-out workflow sends no ambient memory. Then run
   `reliant workflow resume <execution-id>` for each paused run, and
   `reliant trigger activate` for the workflows.
