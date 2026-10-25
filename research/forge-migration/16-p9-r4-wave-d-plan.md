# 16 — P9 R4 wave D: Chat, FileSystem, Forge, Daemon/DaemonRegistry/Token, Settings stores

Status: **plan.** This decides wave D, which `13-p9-frontend-plan.md` D4 left until after wave C ("decided
after C; staying hand-written is acceptable where streaming owns the cache"). Owner decisions are in §8.

Evidence is from reliant `origin/main` at `8ea6d5b6`: wave C is complete (Run #1508, Project #1513,
Worktree #1514, Trigger #1515, Workflow #1516), and so is the forge pin bump to `1dc4e572`
(#1517: `<svc>Queries`, web-runtime `^0.11.0`). Paths are relative to `frontends/web/src/`
unless they start with `proto/` or `forge/`.

---

## 0. Summary

| Service | Verdict | One-line reason |
|---|---|---|
| **ChatService**: list, detail, selection, messages + the writes the stream answers | **stay hand-written** | The stream writes these entries in place and must never be refetched over (`hooks/message-queries.ts:24-38`). Every generated ChatService mutation invalidates `chatServiceKeys.all` (22 of 22 in `hooks/chat-service-hooks_gen.ts`), so a generated key family can't hold them. |
| **ChatService**: archived, search, step/workflow executions, thread inputs, queued agent messages | **move** (lane D3) | These are pull reads that the stream only *invalidates*, which is the wave C shape. |
| ChatService wrapper layering (`api.chatsV2` → `chatGrpc`, `api.toolCalls` → `toolCallGrpc`) | **collapse** (lane D4) | `api.chatsV2` (`api/client.ts:219-420`) is a pure pass-through over `chat-grpc.ts`. |
| StreamingService (`streaming-grpc.ts`) | **stay** | It is the cache's writer. A server stream gets no generated hooks (13 §3.3). |
| ToolCallService | **stay imperative**, wrapper deleted (D4) | Two fire-and-forget commands whose effect arrives over the stream. No cache. |
| **FileSystemService** | **partial** (lane D5): `ListDirectory`/`CreateDirectory` and `SearchFiles` move; tree, content, preview and writes stay | Reads are routed to the chat's own machine and can be large. The tree is fetched lazily, directory by directory, with a client-owned expanded set. The editor buffer is client state that a refetch must never overwrite. |
| **ForgeService** | **move** (lane D2) | No stream touches its cache. It's pull-shaped with errors returned as data. Control-plane reads in the same file stay on hand keys. |
| **DaemonRegistryService** | **move** (lane D1) | The stream only *invalidates* it (`daemons` refetch). Today five hand keys cover one RPC. |
| **TokenService** | **move** (lane D1) | Pull-shaped, with no cache today (`useState` + `useEffect`). The `environments.ts` token helpers are dead code. |
| DaemonService (OAuth helper) | **stay imperative**, wrapper deleted (DZ) | One-shot calls from non-React `lib/*-oauth.ts`. No cache. |
| SettingsService zustand stores | **owner decision S** (§8). Recommended: mirrors become cache projections, client-state stores keep reading through | §6 |
| Stragglers in no wave: Inbox, Connector, Message, Account, ModelEndpoint | **move** (lane D7) | Wave C shape, or single reads/writes |
| `grpc-client.ts` | **delete** (lane DZ, last) | It's a set of names over `connectClient` (its header says R4 deletes it piece by piece). 37 non-test importers, 72 test files. |
| `grpc-unauth.ts` | **stay** | Pre-session calls for the supabase storage adapter. Kept apart for the import graph (its header). The route is decided in `lib/connect.ts`. |

**A gap wave C left, found while planning** (lane D0): the reconnect sweep after a stream outage
(`store/globalUpdatesStore.ts:564-577`) re-reads only `chatKeys.all` and the daemon list. REFETCH
signals aren't replayed on resume (:559-561). So a `worktree_changes`, `file_tree` or `config_health`
that arrives while the stream is down never reaches `worktreeServiceKeys`, `projectServiceKeys`,
`settingsServiceKeys` or `mcpServiceKeys`. Those keys were moved onto stream invalidation in wave B/C. The only thing
that catches it up is a window-focus refetch of an entry already past its `staleTime` (`lib/query-client.ts:8,12`:
30s by default, 5 min for workflows). A tab that stays focused through a network blip never gets it.

Risk: **Medium.** No schema changes. Each lane is one service (or one pair), so recovery is a revert of that
lane. The large risk is churn in chat's test mocks (D4, DZ), not behaviour.

---

## 1. The test: who owns a cache entry?

Wave C moved a service when **the stream only says "something changed"** and the read is a plain
request/response. The generated key is the request, the generated mutation invalidates the service or
entity scope, and the stream calls `streamInvalidation.invalidate(<svc>Keys.all)`
(`store/globalUpdatesStore.ts:126,430-498,1356`). The cache entry is *the server's answer to that
request*, so refetching it is always correct.

A cache is **stream-owned** when the stream *writes the data itself*. Then the entry is no longer
any one request's answer, and an invalidation that refetches it destroys information. Three signs, each
one disqualifying on its own:

1. **The stream patches entries in place** (`setQueryData`) and the code says never refetch.
2. **An entry is assembled from several RPCs and stream events**, so no single request's response matches it.
3. **A read carries the stream's own resume cursor** or a sequence watermark that orders it against stream events.

Chat's core has all three. Nothing else in wave D has any of them.

---

## 2. ChatService

### 2.1 Evidence that the stream owns the core chat cache

- **Messages are patched, never refetched.** `hooks/message-queries.ts:24-38`: `messageKeys.list(chatId)`
  holds an *envelope* `{messages,total,hasMore,oldestSeq}`. "The chat stream patches `.messages` live
  via the helpers below (setQueryData, never invalidate/refetch)". The envelope is a bounded snapshot
  plus pages prepended by `prependMessagesCache` (:234), with metadata from the stream's pagination
  info (`handleChatPaginationInfo`, `globalUpdatesStore.ts:639`). No single `ListMessages` request has
  that response. The paging is `before_seq`, not `page_token`, so forge's generated `…Infinite`
  doesn't apply either.
- **Chat metadata has one stream write path.** `globalUpdatesStore.ts:163-175` `applyChatPatch`
  ("THE single write path for chat metadata updates arriving over the stream") → `patchChatCaches`
  (`hooks/chat-queries.ts:42`). It patches list and detail, and never fabricates.
- **The detail entry is assembled from several sources.** `seedChatDetail` (`chat-queries.ts:80`) is called from
  `chatStore` (every `loadChats` row, `store/chatStore.ts:1235`), `run-queries`, `message-queries`,
  `MobileChatList`, `useChatInputState` and `loadRunContext`. A detail can come from a list row,
  `GetChat`, `StartChat`/`BranchChat` responses or a stream patch. Under a generated key it would be a
  `GetChatResponse` the server never sent: `GetChat` also reports live run state (`proto/reliant/v1/chat.proto`, the
  `forge:custom` comment on GetChat), and a list row doesn't have it.
- **The list carries the stream's resume cursor.** `ListChats` returns `lastUserUpdateSequence`
  (`chat.proto:174`, "with the user update sequence the stream resumes from"). `loadChats`
  feeds it to `globalUpdatesStore.setLastSequence` *before* `connect()` (`chatStore.ts:1263-1274`). Activity
  precedence orders a list watermark against stream events by sequence (`store/activityStore.ts:11-35`).
- **The selection has `staleTime: Infinity`** and stays fresh only through the stream: `CHAT_CONFIG_CHANGED`
  → `setChatSelection` (`globalUpdatesStore.ts:991`), with `markChatSelectionsUnmaintained` when the stream
  is re-scoped (`chat-queries.ts:262-298`).

### 2.2 Why generated hooks can't hold these entries

`ChatService`'s CRUD-named rpcs are all `forge:custom` (`chat.proto`), so no mutation has an entity
scope. **All 22 generated ChatService mutations invalidate `chatServiceKeys.all`**
(`hooks/chat-service-hooks_gen.ts`, `createMutationHook<…>(chatServiceKeys.all, …)` ×22). One of them is
already in use: the Run lane's `useAdoptChat` / `useUnadoptChat` (#1508). If messages, detail or list sat under
`chatServiceKeys`, every adopt, and every `SendMessage` (the hottest write in the app), would refetch every open
chat's message envelope over the stream's patches. That loses the prepended pages and in-flight stream state.
This is exactly what `message-queries.ts` forbids.

Devil's advocate: "keep the generated keys but never use the generated mutations for chat." That
still fails. The key family is shared with `useAdoptChat`, and forge offers no way to keep one read
out of `.all`. It would also push every stream patch into proto response shapes (`GetChatResponse`,
`ListChatsResponse`) that the ~60 readers of the frontend `Chat` type (`types/chat`) don't use. The cost
is high, and nothing works better afterwards.

**Verdict:** list, detail, selection, messages, thread messages, and every write the stream answers
(Start, RunNow, Send, Update, UpdateState, Cancel, Pause, Resume, Archive, Unarchive, Delete,
Dismiss, MarkUnread, Compact, Branch, SetDaemon, UpdateWorkflowParams, UpdateProviderPolicy,
RetryUsageLimitWait) **stay on the hand `chatKeys` / `messageKeys`**. That's within D4's
pre-approval. The forge work that would let them move later is F-D1 + F-D2 (§7).

### 2.3 The ChatService reads that move (lane D3)

The stream doesn't patch any of these. At most it invalidates them.

| Read | Today | After | Stream / write coupling |
|---|---|---|---|
| `ListArchivedChats` | `useArchivedChats`, hand `chatKeys.archived()` (`chat-queries.ts:301`) | `useListArchivedChats` + `select` (entity scope `archivedChat`) | the stream invalidates it at `globalUpdatesStore.ts:740,771,1070`, and so do `useArchiveChat`/`useUnarchiveChat` → `chatServiceKeys.archivedChat` |
| `SearchChats` | imperative `api.chatsV2.search` (`Layout/ChatSearch.tsx:165`, `Mobile/MobileChatSearchTab.tsx:64`) | `useSearchChats` (enabled on the debounced query) | none |
| `ListStepExecutions` | `api/step-executions-grpc.ts` + hand key in `workflow/run/BuilderRunContext.tsx:60` | `useListStepExecutions` + `select: toStepRecord`. **Delete `step-executions-grpc.ts`.** | none (the Run lane noted it as out of lane) |
| `GetWorkflowExecutions` | `hooks/useWorkflowExecutions.ts`, hand key + refcounted `workflow_executions` refetch subscription | generated key. The refcounted subscription stays and invalidates `chatServiceKeys.getWorkflowExecutions(req)` | REFETCH `workflow_executions` |
| `GetThreadWorkflowInputs` | imperative `chatGrpc.getThreadWorkflowInputs` (`Chat/ChatInput.tsx:510`) | `queryClient.fetchQuery(chatServiceQueries.getThreadWorkflowInputs(req))` | none |
| `ListQueuedAgentMessages` | `hooks/queued-agent-messages.ts`, hand `queuedAgentMessageKeys`, poll while running, optimistic `setQueryData` | generated key (entity `queuedAgentMessage`), same poll and the same optimistic patch on the typed key | REFETCH-announced. The hand writes (`CancelQueuedAgentMessage`, `InterruptThread`, `SendAgentMessage` in `QueuedMessages.tsx`, `ChatInput.tsx`, `SpawnToolRenderer.tsx`, `BackgroundWorkPill.tsx`) invalidate `chatServiceKeys.queuedAgentMessage` |

Dead wrapper methods to delete: `chatGrpc.listBranches`, `getUpdates`, `listPlans` (no callers).

**A mixed family needs one rule:** after D3, ChatService has two key families. Every read belongs to exactly one,
and every hand write names the generated scopes it affects (as above). That's the precondition 13 §3.3 attached to
"reads and writes move in the same PR", made explicit for a partial move. The D3 key test pins it.

### 2.4 Layering (lane D4, no behaviour change)

`api.chatsV2` (`api/client.ts:219-420`, 16 files) only forwards to `chatGrpc`, and `api.toolCalls` (:437) only forwards to
`toolCallGrpc` (3 call sites). D4:
- `git mv api/chat-grpc.ts hooks/chat-model.ts` for the types and proto→frontend mappers (pure, like `trigger-model.ts`).
- The RPC functions go to `hooks/chat-actions.ts` on `connectClient(ChatService)` / `connectClient(ToolCallService)`,
  like `worktree-actions.ts`.
- Delete `api.chatsV2`, `api.toolCalls` and `api/tool-call-grpc.ts`.
- 40 importers (25 of them tests) repoint. Tests answer at the transport (`serviceHandlers("ChatService", …)`), not by
  mocking modules.

---

## 3. FileSystemService (lane D5, partial)

Evidence that the stream doesn't own it, but generated query entries are still the wrong container for most of it:

- **Reads go to the chat's machine.** `filesystem-grpc.ts:215-257`: the dedup key includes `chatId` "because the
  server reads from the chat's machine: the same path for two chats can be two different disks". Large trees are
  bounded by `depth` with a server node budget. Content is up to the 1MB Monaco sync bound (`lib/monacoFileSync.ts`).
- **The tree is client-assembled.** `FileBrowser/FileTree.tsx:195-420` keeps `tree` / `expandedPaths` / `loadingPaths` in
  component state. It fetches directory by directory as the user expands (:269, :297) and refreshes only the visible
  directories on a change (:371-410). No single request's answer is "the tree".
- **The editor buffer is client state.** `FileViewerTab.tsx:81-87` holds `content` and `originalContent` (the dirty
  diff). A background refetch replacing either would discard an unsaved edit.
- **There is no React Query today.** The only cache is in-flight dedup maps (`pendingFileTreeRequests`, `pendingRequests`).
  It can't go stale against a generated key, because nothing is cached.

**Moves:**
- `ListDirectory`/`CreateDirectory` (`Projects/DirectoryPicker.tsx`, `OnboardingFlow/useOnboardingComplete.ts`) →
  `useListDirectory` / generated mutation.
- `SearchFiles` (`FileBrowser/GlobalSearch.tsx`, `FindReplace.tsx`, `Mobile/MobileFileSearchTab.tsx`) → `useSearchFiles`.
  `ReplaceInFiles` stays imperative, because it must refresh open buffers.

**Stays:** tree, content, preview info and blob, metadata, save, create, delete, copy and move.

**Structure:** merge `api/filesystem-grpc.ts` into `api/fileSystem.ts` (the one FS module; 15 importers keep their
import) on `connectClient(FileSystemService)`. Delete `filesystem-grpc.ts`. Keep the in-flight dedup maps. Do *not*
replace them with `fetchQuery` + `gcTime: 0`: that saves ~20 lines and puts multi-MB payloads through the cache.

---

## 4. ForgeService (lane D2, move)

- **No stream invalidation.** `globalUpdatesStore.ts` has no forge refs, and no `subscribeToRefetch` call is in
  `hooks/forge-queries.ts` or `components/Forge`.
- **Errors arrive as data.** `classifyForgeResponse(meta, reportJson)`, `ApplyPromoteResult` refused-as-data
  (`api/forge-grpc.ts:186-240`) and `forgeRetry` (`hooks/forge-queries.ts:130`) all map onto `select`, a domain hook
  over the generated mutation (try/catch around `mutateAsync`, as Trigger's domain hooks do) and `retry: forgeRetry`.
- **`forgeKeys` mixes services** (`forge-queries.ts:137-166`). `topology`/`envStatus`/`envShape`/`promotePlan`/
  `deployPlan`/`deployStatus`/`managedSecrets` are ForgeService (move). `cloudEnvs`/`liveView`/`cloudStatus`/
  `cloudPromotions`/`convergences`/`managedSecretVersions` are **control-plane** services, which have no generated hooks
  (13 §3.1). Those stay on a hand `forgeCloudKeys`. ApplyPromote/StartDeploy keep invalidating them in `onSuccess`
  (:807-812, :924-925), because the generated `.all` can't reach them.
- **Proto:** `PlanPromote` and `PlanDeploy` are read as queries (`forge-queries.ts:739-750` with `staleTime: 0, gcTime: 0`,
  and :842), but they're named like writes, so forge generated mutations. They get `idempotency_level = NO_SIDE_EFFECTS`
  **only after the lane confirms the handler has no side effect**, since NO_SIDE_EFFECTS also makes them GET-able.
  `VerifyEnv` stays a user-triggered mutation (its `onSuccess` patches the topology in place, :205-225). Its generated
  `.all` invalidation is acceptable.
- Delete `api/forge-grpc.ts` (9 importers, 6 of them tests).

---

## 5. DaemonRegistry + Token (lane D1, move)

**DaemonRegistry:** the stream only invalidates it. The gateway's `daemons` / `daemon_local_models` refetch
(`globalUpdatesStore.ts:1334-1346`), the Electron daemon-connected event, heartbeat contradiction
(`hooks/useDaemonStatus.ts:36-66`) and the reconnect sweep (:576) all call `invalidateDaemonList`. Nothing patches it.
But there are five hand keys for `ListDaemons({})`:
- `DAEMON_LIST_QUERY_KEY` (`hooks/daemonListQuery.ts:13`);
- `["onboarding","daemons"]` (`useOnboardingQueries.ts:173`);
- `["onboarding","daemons","gate",attemptStartedAt]` (`DaemonConnectingGate.tsx:143`);
- `QK.daemons` and `QK.detailList` (`Settings/cloud/machines.tsx:143-150`).

So:
- One entry, `daemonRegistryServiceQueries.listDaemons({})`, with `select: r => r.daemons`. `invalidateDaemonList` →
  `daemonRegistryServiceKeys.all`. `fetchDaemonList` and `reconcileDaemonHeartbeat` read the typed entry.
- `machines.tsx`'s separate cadence keys collapse into it. The 2s restart poll then refreshes everyone's list
  during a restart only, which is what `docs/design/one-daemon-list.md` asks for (one list).
- The onboarding gate keeps its own poll on the shared entry. It treats `dataUpdatedAt < attemptStartedAt` as "no
  answer yet" instead of keying per attempt. **Load the `onboarding-flow` skill** (a background effect can complete
  onboarding).
- `ClearMachineCaches` (`useMachineDisk.ts`), `SetLocalModelEndpoints`/`RefreshLocalModels` (`LocalModelsSection.tsx`)
  → generated mutations.

**Token:**
- `Settings/TokenSettings.tsx:51-69` fetches into `useState` with no cache → `useListTokens({kind: DAEMON})` plus
  generated create, revoke and update.
- `Projects/SelfHostedDaemonConnect.tsx:189` → `useCreateToken`.
- `listDaemonTokens`/`createDaemonToken`/`revokeDaemonToken` (`services/controlPlane/environments.ts:189-211`) have
  **no non-test caller**. Delete them and their mocks in `Settings/cloud/__tests__/machines.*.test.tsx`.

Routing: both go through `connectClient`, which sends them to reliant's api-server, never admin-server
(`lib/connect.ts:37-50`). `lib/__tests__/connect.routing.test.ts` is the guard, and it must stay green.

---

## 6. The Settings zustand stores

After #1517, `api/settings-grpc.ts`'s reads go through the generated entries (`fetchPreferences`,
`fetchProviderStatuses` → `settingsServiceQueries`) and invalidate them after writes (`settings-grpc.ts:215,254,272`).
So a store and a mounted hook can no longer get *different answers from the server*. But a store that copies
the answer into zustand only refreshes when it reloads. The seven stores:

| Store | Server state it holds | Non-test importers | Kind |
|---|---|---|---|
| `settingsStore` | GetPreferences copy | **0** | **dead**: delete |
| `preferencesStore` | config-scope prefs + hidden workflow/preset slugs (GetPreferences) | 14 (incl. the chat composer: `ChatInput.tsx:391,428`, `useChatInputState.ts:49`, `WorkflowSelector.tsx:69`) | **mirror**: loaded once per mount site (`MCPSettings.tsx:211`, `WorkflowSelector.tsx:75`, `PresetConfigModal.tsx:50`, `MobileNewChat.tsx:134`). It doesn't see a write made through `useUpdatePreferences` or by another device. |
| `privacyStore` | GetPrivacySettings | 7 | **mirror** |
| `bookmarksStore` | `additional.browserBookmarks` + localStorage `persist` | 1 | **mirror** |
| `shortcutsStore` | GetShortcuts overrides merged with static defaults | 7 + the non-React keyboard layer | client state derived from a one-time read |
| `apiKeySetupStore` | provider statuses + a localStorage "dismissed" flag | 9 | UI flow state |
| `browserStore` | tabs; reads the default page once | 11 | client state |

Options (owner decision S, §8):
- **A. Keep the read-through.** Only repoint `api.settings.*` at typed functions in `hooks/settings-queries.ts`
  (on `settingsServiceQueries`), then delete `settings-grpc.ts` and `api.settings`. The mirrors keep their staleness.
- **B. Mirrors become cache projections (recommended).** `preferencesStore`, `privacyStore` and `bookmarksStore` keep
  their selectors, so the 22 consumers don't change. Their state is fed by a query-cache subscription on the generated
  entry, which is the `projectStore` precedent (`subscribeToProjectRows`, `hooks/project-queries.ts:335`, #1513). Their
  writes go through cache functions with optimistic update and rollback. The client-state stores (shortcuts,
  apiKeySetup, browser) keep reading through, as in A. `settingsStore` is deleted. The files stay disjoint from the
  Chat lanes.
- **C. Delete the mirror stores.** Consumers read hooks (`usePreferences`-style). It's the cleanest end state, but it
  touches ~22 consumers, including the chat composer files D3 and D4 own, so it would have to run after D4.

Why B over C: it gets C's correctness (one answer, always current) at A's consumer churn, and it can move to C
later with no throwaway work. Why B over A: A leaves the "two copies" class open, and the `--frontend-stores` lint
can't see it (#1514 friction, F-D6).

---

## 7. Forge work items

No generic "streaming-owned cache" primitive is proposed. reliant's stream protocol is product-specific:
- a per-user sequence cursor that ListChats seeds;
- one chat-detail subscription slot;
- a bounded snapshot plus `before_seq` paging;
- delta resume.

A forge primitive for it would be a *mode* with one user, not a primitive (forge's own philosophy). What
reliant keeps hand-writing *around* the generated hooks is small and general:

| # | Item | Evidence | Needed by | Priority |
|---|---|---|---|---|
| **F-D2** | **Method-scope keys.** `<svc>Keys.<rpc>` should also export the prefix `[service, entity?, method]` as a typed filter (DataTag'd), so `setQueriesData`, `invalidateQueries` and cache subscriptions can target every request variant of one read without hand predicates or tuple positions. | `worktree-queries.ts:444-454` (`requestOf`/`isListQuery` scan key parts), `project-queries.ts:340` (`key[1] === "listProjects"`, which **breaks silently** once a `forge:query-scope` inserts the entity at index 1) | D1, D3, D6-B, every future stream patch | **high** |
| **F-D1** | **Mutation invalidation scope** `// forge:invalidates <Entity>…` / `none` ("the stream answers this write"). #1316 refused `query-scope` on mutations because it "could only narrow". This would make narrowing explicit and lint-visible. | 22/22 ChatService mutations → `.all` (§2.2); Run's `useAdoptChat` already invalidates every chat read | Only if chat writes ever move. Not wave D. | defer (§8 F) |
| **F-D3** | web-runtime **stream invalidation helpers**: a coalesced invalidator, and `readBeforeDisruption` as a predicate for reconnect sweeps | `lib/coalescedInvalidation.ts:49`, `globalUpdatesStore.ts:126,156` | nice-to-have | low |
| **F-D4** | typed **cache subscription** for non-React projections (`onQueryData(filter, listener)`) | `subscribeToProjectRows` (#1513), and D6-B would add three more | D6-B (reliant can hand-write it once; upstream after) | medium |
| **F-D6** | `forge lint --frontend-stores`: flag a store that reaches a server through a project module that itself imports a service descriptor or `connectClient` (one hop) | open since #1514 | after DZ, when only legitimate imperative modules remain | low |

---

## 8. Owner decisions

**S. Settings stores.** Options A, B or C (§6). **Recommendation: B**: mirrors become cache projections, client-state
stores read through, `settingsStore` is deleted. Lane D6 waits for this decision. Everything else proceeds without it.

**F. forge F-D1 (mutation invalidation marker).** (1) Build now. (2) **Defer until a lane proposes moving chat writes
(recommended).** Wave D doesn't need it, and an unused narrowing knob is a staleness hazard with no user. F-D2 isn't
a decision: it fixes a latent silent-break and every stream-patching lane uses it. It goes ahead as forge work.

**Confirm (no action needed unless vetoed):** the chat core stays on hand keys (§2.2), under D4's "acceptable where
streaming owns the cache".

---

## 9. Lanes

Each lane owns the files listed. `store/globalUpdatesStore.ts`, `api/client.ts`, `api/grpc-client.ts` and the shared
test transports (`lib/test-utils.tsx`, `test/*`) are **shared files, with minimal edits only**: one line or one block per
lane, and rebase conflicts expected, as in wave C. Everything else is disjoint.

```
D0 (reconnect sweep) ─┬─► D1 Daemon+Token ─┐
                      ├─► D2 Forge ────────┤
                      ├─► D3 Chat pull ─► D4 Chat layering ─┤
                      ├─► D5 FileSystem ───┤
                      ├─► D7 stragglers ───┼─► DZ delete grpc-client + leftovers
F-D2 (forge) ─ pin ───┘   D6 Settings (after S) ┘
```
D0 is small and lands first, because D1, D3 and D7 each register their scopes in it. F-D2 isn't a blocker: lanes
may use a hand predicate and switch after the pin.

| Lane | Owns (files) | Must add (tests) |
|---|---|---|
| **D0 reconnect sweep** | `store/globalUpdatesStore.ts` (one exported `STREAM_INVALIDATED_SCOPES` list used by both the handlers and the reconnect sweep at :564-577), its tests | `globalUpdatesStore.reconnect-sweep.test`: after a disruption, every registered scope read before the disruption refetches, and one read after doesn't. **This fails on main today** for `worktreeServiceKeys` / `projectServiceKeys` / `settingsServiceKeys`. |
| **D1 Daemon + Token** | `hooks/{daemonListQuery,useDaemonStatus,useOnboardingQueries,useDaemonWait,useMachineDisk}.ts`, `components/OnboardingFlow/{DaemonConnectingGate.tsx,commitLaunchPlan.ts,addRepoProject.ts}`, `components/Projects/{ConnectDaemonModal,SelfHostedDaemonConnect}.tsx`, `components/Settings/{LocalModelsSection,TokenSettings}.tsx`, `components/Settings/cloud/machines.tsx` (+ its `__tests__/machines.*`), `services/controlPlane/environments.ts` (token block), new `hooks/{daemon-registry,token}-queries.ts` | `daemon-registry-keys.test`: (1) a `daemons` refetch refetches every mounted reader with one request; (2) heartbeat contradiction reads the typed entry; (3) the onboarding gate ignores data older than the attempt; (4) a `ClearMachineCaches` refetches the list. `token-keys.test`: create/revoke refetch the mounted list. `connect.routing.test` stays green (both reliant-routed). |
| **D2 Forge** | `api/forge-grpc.ts` (delete), `hooks/forge-queries.ts`, `components/Forge/{Deploy/DeployFlow,Deploy/DeployRefusalNotice,Promote/PromoteFlow,Promote/PromoteRefusalNotice}.tsx`, `proto/reliant/v1/forge.proto` (+ regen) | `forge-keys.test`: (1) ApplyPromote applied refetches ForgeService reads *and* the control-plane `forgeCloudKeys`; (2) a refusal returns `{kind:"refused"}` and writes nothing; (3) `forgeRetry` retries once and never on the non-retryable codes; (4) plan reads aren't cached (`gcTime: 0`). A Go test that PlanPromote/PlanDeploy are side-effect free if they get NO_SIDE_EFFECTS. |
| **D3 Chat pull reads** | `hooks/{queued-agent-messages,useWorkflowExecutions}.ts`, `hooks/chat-queries.ts` (archived only), `components/workflow/run/BuilderRunContext.tsx`, `api/step-executions-grpc.ts` (delete), `components/Layout/ChatSearch.tsx`, `components/Mobile/MobileChatSearchTab.tsx`, `components/Chat/{ChatInput.tsx (thread inputs + agent send), QueuedMessages.tsx, BackgroundWorkPill.tsx, tool-renderers/SpawnToolRenderer.tsx}`, `api/chat-grpc.ts` (moved and dead methods only), globalUpdatesStore archived lines | `chat-pull-keys.test`: (1) archive/unarchive (hand) refetches the mounted archived list, and so does the stream's archive event; (2) cancelling a queued message patches, then refetches; (3) a `workflow_executions` pulse invalidates once for N readers; (4) **a SendMessage refetches none of the moved reads**, and **`useAdoptChat` doesn't refetch the message envelope** (pins §2.2); (5) the reconnect sweep covers `chatServiceKeys.all` (D0 registry). |
| **D4 Chat layering** (after D3) | `git mv api/chat-grpc.ts hooks/chat-model.ts`, new `hooks/chat-actions.ts`, `api/tool-call-grpc.ts` (delete), `api/client.ts` (`chatsV2`/`toolCalls` blocks), the 16 `api.chatsV2` callers, `store/chatStore.ts` (imports only), the 25 chat tests | No behaviour change: the existing chat suites pass answered at the transport, with no `vi.mock("…/chat-grpc")` left. `chatBigintSafety`-style guard repointed at `chat-model.ts` if one exists for the wrapper. |
| **D5 FileSystem** | `api/filesystem-grpc.ts` (merge → delete), `api/fileSystem.ts`, `components/Projects/DirectoryPicker.tsx`, `components/OnboardingFlow/useOnboardingComplete.ts`, `components/FileBrowser/{GlobalSearch,FindReplace}.tsx`, `components/Mobile/MobileFileSearchTab.tsx` | (1) the directory picker refetches after CreateDirectory; (2) search is keyed by query and doesn't refetch on unrelated writes; (3) the existing dedup tests: two concurrent `getFileTree` for one (project, worktree, chat, path, depth) make one call, and two chats make two; (4) a ReplaceInFiles refreshes an open, clean buffer and leaves a dirty one alone. |
| **D6 Settings** (after S) | `api/settings-grpc.ts` (delete), `api/client.ts` (`settings` block), `hooks/settings-queries.ts`, `store/{settingsStore (delete),preferencesStore,privacyStore,bookmarksStore,shortcutsStore,apiKeySetupStore,browserStore}.ts`, the 17 non-store `api.settings` callers (`CombinedGeneralSettings`, `MobileAIProvidersPanel`, `services/settingsSync.ts`, `lib/settingsPersistence.ts`, `lib/{copilot,codex,claude,antigravity}-oauth.ts`, `lib/open-link.ts`, …) | Under B: (1) a `useUpdatePreferences` write reaches `preferencesStore` without a reload, and so does a refetch carrying another device's change; (2) a refused store write rolls back; (3) bookmarks survive a failed write; (4) privacy initial load still retries until config is ready. `forge lint --frontend-stores` stays clean. |
| **D7 stragglers** | `hooks/inbox-queries.ts` (+ one globalUpdatesStore line at :441), `components/Settings/cloud/machineAccess.tsx`, `components/Settings/ConnectorConsent.tsx`, `components/workflow/NodeDetailsPanel.tsx` (GetMessage), `components/Settings/DeleteAccountDialog.tsx`, `components/Settings/CustomEndpointsSection.tsx` (ModelEndpoint) | `inbox-keys.test`: dismiss/restore refetch the list, and the stream's inbox-invalidating update reaches `inboxServiceKeys.all`. `connector-keys.test`: create/revoke refetch the connectors read. |
| **DZ cleanup** (last) | `api/grpc-client.ts` (delete), `api/daemon-grpc.ts` (delete → `connectClient(DaemonService)` in `lib/oauth-local.ts`; load `oauth-provider-login`), `api/process-output-streaming.ts`, `api/streaming-grpc.ts` (transport import only), `api/scenarios.ts`, `lib/{node-metadata,cel-completion-service}.ts`, `components/workflow/config/ConfigPanel.tsx`, `api/client.ts` (`models`/`tools` → `catalog-queries`; delete the file when empty), `hooks/useAuthMode.ts`, `components/{Terminal/Terminal,Automations/ActivateTriggerDialog,Chat/MessageGeneratedVideos}.tsx` (→ `reliantApiBaseUrl`), the remaining test files mocking `grpc-client` | `rg "grpc-client"` returns nothing. The remaining mocks answer at the transport. `connect.routing.test` is green. `tsc -b` 0. |

Optional, not scheduled: `globalDataStore.models` is a zustand mirror of Catalog `ListModels`, refetched by
the `daemon_local_models` stream signal (`globalUpdatesStore.ts:1344`). It's the same mirror shape as §6. Do it as
a follow-up after D6, in the same style.

### 9.1 Risks and pitfalls (from wave C's PR bodies, plus new ones)

- **The mixed key family (D3):** a hand chat write that forgets to name a moved generated scope leaves it stale. The D3
  key test pins each write → read pair.
- **NO_SIDE_EFFECTS flips** (D2) change the hook kind. The owned `forge-service-hooks.test.tsx` starter row goes stale.
  forge#1316 now reports it in `forge generate` output and in `forge lint --conventions`. Fix the row in the same commit
  (C-Run, C-Trigger).
- **Hard-coded key positions:** `project-queries.ts:340` and `onboardingChecklistStore.ts:499-500` read `queryKey[1]`.
  Don't add `forge:query-scope` to those services until F-D2 lands, and don't copy the pattern.
- **Cold-import timeouts:** a store test that re-imports after `vi.resetModules()` crosses 5s under load. Import once in
  `beforeAll` (#1515).
- **A mocked hook returning a fresh object each render** re-derives forever (#1516). Mocks return one stable object.
- **Imperative reads** use `<svc>Queries.<rpc>(req)` (#1517). Never hand-copy a key plus fetch.
- **The onboarding gate** (D1): a background effect can complete onboarding with no user action. Load `onboarding-flow`.
- **Routing** (D1): Token/DaemonRegistry must stay reliant-routed. `connect.routing.test.ts` is the guard.

### 9.2 Gate per lane

`npx tsc -b` = 0. `npx vitest related --run <changed files>`. `forge lint --frontend-stores` with the pinned forge.
`task generate:forge` (or `make forge-generate-check`) after a proto change. Merge through
`.fm-tools/merge_queue3.sh <branch>`.

**Recovery:** roll forward. Each lane is one revertible PR per service, with no schema changes. The NO_SIDE_EFFECTS
proto changes are additive at the wire (GET becomes allowed, POST still works).
