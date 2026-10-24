# Provider switch by original tags

## Scope and status

Checkout: `provider-switch-by-tag`, branch `fix/chat-provider-switch-preserve-tags`.
Base main `9f51649e`; no overlapping provider-switch fix in main through that commit.
Owner requested root exact selection unchanged, descendants on original semantic tags constrained to chosen provider, including active/waiting/stopped/resumed/nested/future descendants.

Skills read: debug (triage/discipline), testing-methodology (test harness/tiers). Implementation and verification underway; see approved policy and tests below.

## Verified root cause

1. `handlers/chat/chat_workflow.go:UpdateWorkflowParams` sends only the requested `model` via `update_workflow_state` and then a capacity wake with `UseModel` set to the resolved root concrete ID@driver.
2. `runtime/workflow.go:setupInputUpdateHandler` skips global `model` updates for thread-owned inputs. Preset models are owned (deliberately; stamping root's concrete model onto them is wrong).
3. `runtime/provider_preference.go:propagateProviderPreference` copies the parent's preference during child construction only. Existing descendants keep the old `__provider_preference`. Explicit provider choices inside a descendant supersede it.
4. `runtime/usage_limit_wait.go` handles wake.UseModel by creating a concrete fallback lease; it replaces `call_llm`'s selector with the root's concrete model. This does not preserve original semantic tags.
5. `activities/handlers/llm_request.go:withRunProviders` only adds providers when a selector has none; explicit pinned IDs/providers are left alone. Even when applied, `registry_v2.go:Resolve` falls through from preferred providers to *any configured provider* when no tag candidate on a preferred provider serves. Therefore `Providers` is NOT a strict constraint despite the task's initial expectation. Constraining availableProviders to only the selected driver is required at the resolution boundary (and fallback/tag-preference paths must honor it too).

## Original selectors survive

- `builtin/presets/general.yaml`: `params.model.tags: [flagship]`.
- `InlineWorkflowExecutor.buildSubWorkflowInputsWithOwnership` layers preset params, passthrough, explicit args, and schema defaults. It retains the selector object. It doesn't replace it with resolved concrete model IDs.
- `CallLLMActivity` builds a `models.ModelSelector` from evaluated proto ID/Tags/Providers each call; registry resolution is local to the activity. Resolution doesn't mutate workflow input maps.
- `ChildWorkflowTracker.threadInputs`, `lastThreadInputs` retain those maps; per-thread edits survive in `threadInputOverrides`, carried through ContinueAsNew.
- `SpawnHandoff.ChildInputs` stores node args; resumed spawns rebuild preset inputs via `prepareSpawnRelaunch`. Durable stopped-spawn resumes read original spawn tool input and reload the preset. Any new chat provider constraint must therefore be a separate runtime-injected input inherited regardless of a child's original provider preference, not a destructive edit to its semantic selector.
- A preset's tag can be overwritten by an exact model arg. To prefer that original preset tag when switching providers, capture it immediately after preset load and before overlayNodeArgs. Do not reverse-infer a tag with registry.TagsOf: one concrete model can satisfy multiple tags.

## Exact-model descendants: existing semantics and approved policy

Existing product semantics explicitly protect preset params and node args against global root model changes (`threadOwnedKeys`). `provider_preference.go` documents spawn args > preset > inherited and preserves a child's exact provider pin. `buildSpawnChildInputs` deliberately excludes the parent's model and thinking. Thread-scoped UI overrides are remembered and reapplied on resumption.

Owner answered `4764621b-b7a0-4c4b-b5d7-358248522d1b`: visibly HOLD incompatible exact-only descendants and unsupported tag mappings, while root and supported descendants continue. If the exact same model is available on the selected provider, use it unchanged except driver; otherwise never guess a tag. Recover a preset's original tag when available. A later compatible edit or user resume retries the held model call.

Owner answered `39994154-55e8-4d71-a93d-7fd9699b4197`: if an explicit ROOT picker choice is unavailable, REJECT the picker change before writing the selection or sending any switch signal. This replaces the existing silent automatic substitution for explicit picker edits; ordinary send-time recovery is unchanged.

Owner also clarified: resuming any portion resumes the whole unfinished chat. User doorbells and paused-message resumes now wake all unfinished threads; stopped descendants can be discovered via the existing LookupStoppedSubAgent activity's new `all` request. Completed and cancelled agents are excluded. No tool cancellation is part of provider switching.

## Registry identity (not guessed)

The displayed GPT-6.1 Sol corresponds to `gpt-6.1-sol`, served on `codex` AND `openai` (and advertised elsewhere in catalog). The displayed name alone cannot identify the chosen driver; use the actual selector and `Launcher.ResolvedModelAtDriver` to identify the selected driver. `sept_2026_models_test.go`, `registry_v2_test.go` pin the mappings; `constants.go:GPT61Sol` defines the ID. Real semantic tags include flagship, fast, moderate. gpt-6.1-sol itself can appear under multiple tags, confirming reverse inference is wrong.

## Thread account pins

`thread_provider_accounts` is keyed by (thread_id, provider). `accountrouter.Assign` checks the stored assignment against eligible accounts and model compatibility each call; it reassigns exhausted/removed/unserved accounts. Cross-provider old assignments cannot serve another driver, but task explicitly requests clearing incompatible pins before next assignment. Clear only the old-provider assignment(s) for the thread, not the account's limits or the user's routing policy. Do not interrupt tools; do at next model-call activity boundary.

## Test infrastructure

- `runtime/subAgentFailureEnv` drives real DynamicWorkflow with deterministic fake activities, records call RuntimeContexts, supports root + nested spawn scripts, usage-limit waits and stopped resume paths.
- `handlers/chat` selectionFixture records actual update/wake signals. Add handler-to-DynamicWorkflow bridge test rather than merely inspecting resolver arguments.
- `activities/handlers` resolver tests can use the actual registry and fake available drivers/routing store.
- `runtime/replaytest` real Temporal histories current+frozen; adding runtime provider propagation or new branches scheduling commands requires GetVersion. Old shape should include model change while tag-owned spawns wait. Record old history before edits; freeze bytes, then demonstrate unversioned replay fails and versioned passes.
- Repo main now uses forge pgtest (private DB per test), not DATABASE_URL. Never use real data on 5434. Use private TMPDIR for embedded PG on this shared machine; previous task saw /tmp swept mid-test.
- Run make test-affected, scoped make test-short, touched-package full tests once, changed tests 3x. Never make test/test-all.
