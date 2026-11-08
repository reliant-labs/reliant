# Ambient memory exclusion — task40e5b620

Owner approved narrow API, 2026-11-07. Work ONLY in this registered worktree, never shared checkout. No merge/release/gates/daemon updates. Growth AI stays disabled pending complete context-boundary proof and runtime deployment.

## Settled findings and contract

- Current origin/main at creation: 22a9dd7a3; still unconditionally injects global/project `formatStoredMemories` and repo `formatRepoMemoryMessages` in `internal/workflow/runtime/activities/handlers/call_llm.go` near 1484.
- Public API approved: `exclude_ambient_memory: true` on `CallLLMArgs` and `ThreadConfig`. Native bool, default false; inherited exclusion is monotonic OR, descendants cannot reenable it with false/unset.
- Carry inherited policy via ExecutionContext and activities/types.RuntimeContext, all nested workflow/router/loop/parallel/spawn boundaries and retry serialization. No tool permission changes.
- Excludes ONLY automatically injected global/project/repo memory blocks. NOT full model context isolation or a read-access restriction.
- Existing bootstrap `getSystemPrompts` includes identity/safety text, working directory, multi-repo metadata, static Reliant capabilities, skill catalog and explicit system_prompt; other notes/preloaded skills/history/args.messages/tool schemas remain. Inventory exact remaining sources with tests.
- Capture real outgoing provider request from CallLLM handler, private global/project/repo canaries absent when opted out, default retains them, approved explicit brief present, retries and inherited exclusion covered.
- Verified current API uses NoMachine project with empty config, tools_config.preloaded_tools: [] AND loadable_tools: [], skills: []. Tool array is empty but bootstrap remains; skill catalog persists with populated config. Projectless launch is rejected (project_id required). See ambient-memory-context-inventory.md.

## Implementation record

CLI-delegated helpers were routed to another machine and could not access this checkout. Main agent performed implementation; no owner/shared checkout edits. Friction report: 817d05ff-7f01-4403-8689-546bf7942e39.

Owner explicitly approved additive durable thread policy after discovery that coarse recovery discards runtime maps. Migration 20261108064904_thread_ambient_memory_exclusion.up.sql adds false-default boolean. Thread creation latches in the existing transaction; CallLLM reads recursive lineage and latches before provider send, fails closed on errors. API false/unset cannot reset an excluded thread. Runtime contexts/continue-as-new also carry policy.

Scoped short suites passed: handlers, runtime, threads, store, db, workflow/yaml, runtime/schema. New runtime/provider/store tests repeated 3x passed. Provider tests include real OpenAI HTTP request capture, driver-boundary retry capture, fresh-handler recovery, stopped-child lineage, minimal machine-less context. No whole-repo gates or daemon updates.

## Independent review repair

Review of 9682c2f4699f264346bb81045b36d21c011108f8 reproduced a dangling-parent
policy bypass: counting surviving lineage nodes did not prove ancestry complete.
The resolver now requires a real root, tracks visited IDs, and bounds traversal
at 256 threads; dangling parents/grandparents, cycles and depth truncation fail
closed. Actual CallLLM regressions populate all three private-memory canaries and
assert no driver request for each malformed lineage, with both false and true
surviving policies. Complete cross-chat forks remain supported; an already-true
row is no longer rewritten each turn.

## Verification/shipping

Only scoped fast tests and build for affected packages. No whole-repo tests/gates. New tests deterministic repeated 3x. Git commit identity through env GIT_AUTHOR_NAME/GIT_COMMITTER_NAME=reliant-01 and EMAIL=sean@reliantlabs.io. Non-draft upstream PR no labels; report and stop. No daemon update.
