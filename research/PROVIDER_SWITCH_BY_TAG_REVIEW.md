# Independent review: provider-switch-by-tag

Reviewed commits `ae3c22b2` and `f0a4da85`. Verdict: **request changes**. The reviewer did not run tests: no heavy slots were available.

## Blocking (owner decision needed before implementing)

### B1: almost every chat gets the strict binding
`internal/launch/provider_policy.go:94` adds `__chat_provider` to any fresh run whose stored selection has a `model`. The composer stores `workflowParams.model` on ordinary sends. Once a chat is bound:
- tag preferences on other providers are ignored, because `resolveSelector` and the strict available providers block `PreferredModelOn`;
- `resolvePinnedLocalPref` is skipped, and local descendants are held at `llm_request.go:219`;
- usage-limit failover is disabled (`usage_limit_wait.go:406/:492`, `call_llm.go:316`).

The handler and the launcher bind tag-only picks too, while the runtime's `selectedProvider` comment says tag-only picks are not a switch.

**Fix direction (owner choice):** bind only on an explicit provider switch, or store the binding as its own visible field. Add a test that a fresh chat with a stored pick keeps tag preferences, failover and local presets.

### B2: local root pick refused, or held on a bound chat
`launch/selected_model.go:30` resolves only through the registry. A local selector (`@local`, or a pinned daemon or endpoint) therefore resolves to nil and gets FailedPrecondition. If it gets past the picker on a bound chat, there is no registry driver, `chatProvider` stays at the old value, and the root holds at `llm_request.go:219`.

**Fix direction (owner choice):** validate with the local resolver, and treat a local root pick as clearing the binding. Add a test.

## Major

1. **The binding is computed before send-time recovery moves the model.** `chat_send.go:413` injects the binding before `fallBackFromUnservableModels` at `:432` mutates `initialData["model"]`; the same happens at `:1157`. So the binding can point at driver A while the moved model is on driver B, and the root's first call holds. Compute the binding after the moves, from the final inputs.
2. **Fork/passthrough descendants no longer follow the root model.** `workflow.go:3019` skips the global model update for every non-root thread, including passthrough fork threads (`mode: fork`, `passthrough: [model]`). Affected builtins: landing-page `style_pass`, forge-one-shot `launch_preview`, and all forge-migrate nodes. They keep the old exact model without `__preset_model`, are treated as exact-only, and are held. Skip only keys the thread owns (`threadOwnsKey`).
3. **Compaction doesn't follow the `call_llm` rules.**
   - `step_executor.go:1190` passes `ChatProvider` but not `PresetModelTags`.
   - `compact.go:396` marks every compaction, including the root's, as a descendant.
   - `resolveCompactionModel` can choose a fallback on another provider, which then holds.

   A thread held at compaction cannot progress until the provider changes. Pass `Descendant`/`PresetModelTags`, mark root compaction as root, and restrict the fallback to the bound provider.
4. **Every user message runs a whole-chat reset** (owner policy for semantics; technical cost below).
   - `workflow.go:2913–2923`: every `FromUser` doorbell clears all fallback leases (`:1327`), wakes every usage-limit wait, and runs the all-stopped lookup.
   - The lookup is N+1: `ListThreadsByConversation` plus `GetStoppedSpawnAgent` per thread, inside a 10s marker activity.
   - On unbound chats, a #1371 lease thread goes back to its limited provider and posts a fresh switch_provider notice on every message.
   - Deterministically failed agents are rerun, and their parents get new `#resume-N` failure reports and extra turns.
   - Owner decision needed: should terminally failed agents be retried on every message? Technically, clear leases only on bound or actual switches, and use a single-query lookup.
5. **The run can end and drop siblings the lookup revives.** `stoppedAgentsAwaitingResume` (`subagent_resume.go:227`) snapshots its candidates before `Await(!stoppedLookupsInFlight)` at `:239`. Siblings that `reviveDurableStopped` revives during the await are refused because the run is ending, and they aren't in the snapshot. Rebuild the candidates after the await.
6. **`GetVersion` markers grow without bound.** `resume-whole-chat/N` runs on every message. Each ID upserts `TemporalChangeVersion`, so after about 30 IDs the SDK's 2 KB search-attribute limit is hit (`internal_event_handlers.go:994`), with warnings and lost visibility. Replay is correct but wasteful. Cache the first non-default answer per category in the tracker and skip later `GetVersion` calls (replay-safe).
7. **A thread-scoped cross-provider pick is accepted, then held** (owner policy). `switchSelector` rewrites `claude@anthropic` to `claude@codex`, and the thread-edit path returns before `ValidateSelectedModel`. Reject the pick, or have the owner decide that per-thread overrides are allowed.
8. **A tag-only root pick binds a provider the user never chose** (`chat_workflow.go:142`). Its resolve ignores tag preferences and can differ from what an unbound root resolves to. Same root cause as B1.

## Minor
- Account pins: `account_routing.go:157` runs a DELETE on every bound model call. Clear them once per switch or epoch instead.
- `provider_switch.go:139`: every held retry writes a new `WorkflowError` card. Use a stable ErrorID per thread.
- `ValidateSelectedModel` isn't strict when a selector has both tags and providers: the registry can fall through, and the binding follows it.
- An explicit pick skips `moveUnservableModels` for an unservable group `<group>.model`.
- The `reviveDurableStopped` lookup can block the doorbell/update handler loop for up to 10s × 3 attempts, and its failure is only logged.

## `f0a4da85` ResumeAllStopped
- Determinism is fine: only new inputs carry the field, and per-wake versioning still applies. Continue-as-new and late-wake successors don't carry the one-shot field, which is correct.
- No double resume of the requested agent: `resumeStopped(requested)` registers it as live before the lookup, and adoption refuses live threads. Nested depth-2 agents are picked up in the sorted pass afterwards.
- Test gap: `TestFreshPortionResumeAlsoResumesDurableSiblings` omits `requested` from the fake `all` list. The real DB would return it, so dedupe isn't proven. Include it and assert one turn each.
- Confirm that `AwaitSpawnsFirst` now also waits on revived siblings.

## Checked OK
- The hold survives the activity boundary: `flattenForTemporal` lifts the wrapped error to a top-level non-retryable `ProviderSwitchHold`. No test covers the real wrapper; add one.
- A hold is visible, per-thread and non-terminal, and doesn't busy-loop. A wake, message, pause or cancel retries it.
- The old-history update gate is proven by the frozen fixture.
- No duplicate within one execution: the liveness check and registration happen with no yield in between.
- A real-DB test covers pin clearing.
- Client-sent `__chat_provider` is stripped at `launch/workflows.go:353`.

## Tests to add
- A fresh chat with a stored pick keeps tag preferences, failover and local presets.
- A send-time move, then the first call.
- A fork passthrough model across a switch.
- Compaction with recovered preset tags on a bound chat.
- A revived sibling while the run is ending.
- A local root picker.
- An end-to-end hold through the real activity wrapper.
- Lease survival across a user message on an unbound chat.
- `ResumeAllStopped` dedupe with `requested` included in the lookup results.
