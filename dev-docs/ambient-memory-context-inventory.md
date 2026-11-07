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
DEFAULT false`, migration `20261107122606_thread_ambient_memory_exclusion.up.sql`.
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
