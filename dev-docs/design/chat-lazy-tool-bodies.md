# Lazy tool bodies: open a chat without its tool inputs and outputs

Status: **design, owner-approved direction (2026-11-08); five decisions are
open (see the end).** No production code yet. Branch
`feat/chat-lazy-tool-bodies`.

The direction: a chat opens without the full inputs and outputs of its
finished tool calls. The client fetches a call's body when the user expands
its fold or its row. Detailed mode and the simplified mode (being reworked on
`fix/chat-fold-improvements`) keep working without fetching anything.

## What already exists (and what this builds on)

#1680 (`d20f53f90`) did the first half of this, for results only:

- Each tool result goes over the wire once, as its own `tool_result` block
  (`matched_result`, tag 9, is reserved). The client joins a call to its
  result by `tool_call_id` through `toolResultsByCallId` (in
  `@reliantlabs/sdk/client`, `tool-results.ts`).
- Every client-facing read caps a result at **8 KiB** (`ToolResultPreviewBytes`)
  as a head-and-tail preview, marked `truncated` with `original_size`
  (`internal/messageproto/preview.go`). A JSON object stays valid JSON with its
  long strings shortened, so the shell renderer can still parse it. Some tools
  are exempt and sent whole (`structuredResultTools`: the plan and task
  tools, `ask_user`, `request_machine`).
- `ChatService.GetToolResult(chat_id, tool_call_id)` returns the full result.
  The web client fetches it on "Show full output", on copy and on export
  (`hooks/tool-result-queries.ts`, `staleTime: Infinity`).
- **Tool inputs were left uncut on purpose** ("every renderer parses them").
  Inputs are now the only unbounded part of a transcript: a `write`'s
  `content`, an `edit`'s strings, a `patch_text`, a heredoc `command`, a
  spawn's prompt.
- Thinking blocks (`THINKING`, `REDACTED_THINKING`) are sent whole on every
  read, but **nothing in the web app or the TS SDK renders them**
  (`rg ContentBlockType.THINKING` finds nothing outside generated code). The
  redacted kind is provider ciphertext.

This design adds a per-request opt-in. With it, a read leaves out the bodies
of finished tool calls, and thinking content, and sends a small stub in their
place. A batched RPC returns those bodies on demand.

## Every path that delivers messages

| Path | Server | Web caller | Today |
|---|---|---|---|
| `ChatService.ListMessages` | `handlers/chat/rpc_list_messages.go` → `messageproto.AssembleForDisplay` | cold seed `seedChatMessages`, scroll-back `loadOlderMessages`, a thread read `fetchThreadMessages` (spawn preview, thread view), `ChatSearch` "current chat" (**unbounded**, reads TEXT only), `useFirstPrompt` (run detail), and the SDK `ChatSession.loadOlder` | inputs whole, results at 8 KiB |
| `MessageService.GetMessage` | `handlers/message/message.go` → `messageproto.FromDB` | `NodeDetailsPanel` (`useGetMessage`, reads the saved text only) | same |
| `StreamingService.StreamChatUpdates` snapshot | `handlers/streaming/streaming.go` `buildChatSnapshot` → `messageproto.Assemble` | SDK `chat-stream.ts` `openStream` (the web wraps it) | same |
| `StreamChatUpdates` replay, `(since_seq, latest]` | `chat_stream.go:441` `chatUpdateToProto` → `formatChatUpdateDataJSON` → `PreviewMessageUpdate` | same stream | the message JSON as written, results at 8 KiB |
| `StreamChatUpdates` live | `chat_stream.go:302` `sendSingleChatUpdate` → same converter | same stream | same as replay |
| `ChatService.GetChatUpdates` | `handlers/chat/chat_crud.go:757` → `PreviewMessageUpdate` | none in web; the Go SDK's follow engine (`sdk/go/reliant/source.go`) | same |
| `GetToolResult` | `rpc_get_tool_result.go` | full result on demand | full |

Nothing else returns a `Message` or a `ContentBlock`. `rg` finds only the
three `Message`-carrying responses and the two JSON update carriers.
`SearchChats` searches on the server and returns `Chat` rows. `BranchChat`
copies history on the server. Electron hosts the web app and makes no message
RPCs of its own. There is no native mobile client: mobile is the responsive
web app.

## How big the win is

**The real-data measurement could not be run.** The control-plane dev
Postgres (localhost:5434, db `reliant`) is not running on this machine: the
connection was refused, and no control-plane compose container exists. The
only other copy of real data was reliant's own `reliant_pgdata` volume. I
copied it into a throwaway container, so the original stayed untouched, and
then deleted the copy. Its `reliant` database had no tables, and its test
databases had no rows. So the numbers below are an analytic estimate. Phase 0
adds the telemetry that measures this on real traffic. The queries to run
against 5434 are in the appendix; I checked them against a scratch schema.

The estimate starts from #1680's seeded window: 200 messages with 50 tool
calls (10 results of 50 to 200 KB, 20 of 6 KB and 20 of 500 B). Its snapshot
went from 2,655,510 to **256,649 bytes** after the 8 KiB preview. Of what
remains, about 215 KB (84%) is tool-result previews (10 × 8 KiB +
20 × 6 KiB + 20 × 0.5 KiB). Under STUB mode, the 30 results over 1 KiB become
stubs of about 1 KiB each, which leaves about 80 KB: **about 3× smaller than
today**. That is a lower bound, because the seed had small inputs and no
thinking. Real agent chats add inputs (a 3 KB `edit`, an 8 KB `write`, a
heredoc `command`) and thinking, and both go to near zero under STUB. The
unbounded reads (`ChatSearch` "current chat", which ships the whole history to
search its TEXT blocks) shrink with the size of the history.

Wire compression is not configured anywhere in this repo (`rg -i gzip`), so
whether the ingress gzips is unknown. That changes the transfer time, not the
work done. The server still marshals the bodies and the client still parses
them, keeps them in React Query and the store, and runs `JSON.parse` on every
input in `processMessage`. Phase 0 measures uncompressed bytes per response.

## Design

### 1. Protocol (additive only)

```proto
// chat.proto
// How a transcript read sends the bodies of finished tool calls.
enum ToolBodyMode {
  // Today's behaviour: inputs whole, results previewed at 8 KiB. Every
  // client that does not set the field gets this, forever (v1).
  TOOL_BODY_MODE_UNSPECIFIED = 0;
  TOOL_BODY_MODE_FULL = 1;          // the same, said explicitly
  // Finished calls' bodies are left out (ContentBlock.body_elided); the
  // client reads them with GetContentBlockBodies when it shows them.
  TOOL_BODY_MODE_STUB = 2;
}

message ContentBlock {
  ...
  // The body of this block was left out of a read that asked for
  // TOOL_BODY_MODE_STUB. tool_call: input holds a skeleton (the same JSON
  // object, every key kept, long strings cut) that is enough to label the
  // call but not to render it. tool_result: content holds a stub preview,
  // and truncated/original_size are set as for any preview. thinking:
  // content is empty. GetContentBlockBodies returns the body by block id.
  optional bool body_elided = 16;
  // Byte length of the stored body (input for tool_call, content
  // otherwise). Set when body_elided is.
  optional int64 body_size = 17;
}

message ListMessagesRequest       { ... optional ToolBodyMode tool_bodies = 5; }
message GetMessageRequest         { ... optional ToolBodyMode tool_bodies = 2; }   // message.proto
message StreamChatUpdatesRequest  { ... optional ToolBodyMode tool_bodies = 4; }   // streaming.proto
message GetChatUpdatesRequest     { ... optional ToolBodyMode tool_bodies = 3; }

// GetContentBlockBodies returns the bodies of content blocks that a
// transcript read sent elided, by block id. A block resolves when it belongs
// to chat_id, or to history a branch of it inherits (the same rule as
// GetToolResult). Any other id comes back in not_found, so the response never
// says whether an id exists elsewhere.
// forge:custom — owner-checked batch read of block bodies, branch inheritance resolved
rpc GetContentBlockBodies(GetContentBlockBodiesRequest) returns (GetContentBlockBodiesResponse) {}

message GetContentBlockBodiesRequest {
  string chat_id = 1 [(buf.validate.field).required = true];
  repeated string block_ids = 2;  // at most 100
  // Results whole rather than as the 8 KiB display preview: for copy and
  // export. Unset returns what an expanded row shows.
  optional bool full = 3;
}
message ContentBlockBody {
  string block_id = 1;
  optional string input = 2;          // tool_call: the stored input, whole
  optional string content = 3;        // tool_result: display preview (or whole when full); thinking: whole
  optional bool truncated = 4;        // as ContentBlock.truncated
  optional int64 original_size = 5;
}
message GetContentBlockBodiesResponse {
  repeated ContentBlockBody bodies = 1;
  repeated string not_found = 2;
  // Left out to keep the response under its byte budget: ask again.
  repeated string deferred = 3;
}
```

Why it is built this way:

- **Bodies are keyed by block id, not tool_call_id.** A tool-call id is the
  provider's: it is not unique across chats (`GetToolResult`'s own comment),
  and a sequential-id provider can repeat one within a chat. A block id is a
  primary key, and one RPC then serves inputs, results and thinking alike.
  The client already holds both ids: `ToolCallData.content_block_id` for the
  call, and the result block's id, which `ResolvedToolResult` will gain.
- **An elided result is also `truncated`.** Copy and export already turn
  `truncated` into a full fetch. So even a code path that never learns about
  `body_elided` can never copy a stub as if it were the output.
- **The field is a mode enum, not a bool.** It leaves room for a third mode
  later (for example, stubbing live payloads too) without another field.
  Unspecified means FULL.
- **The response cap uses deferral.** Bodies are added until the response
  would pass 4 MiB, and the remaining ids come back in `deferred`. At least
  one body is always returned, so the client always makes progress. More than
  100 ids is `INVALID_ARGUMENT`.

`buf.yaml`'s `breaking: FILE` and the merge gate's `buf breaking` both accept
this: a new enum, new fields, and a new rpc and its messages.

### 2. Server: what a STUB read leaves out

All of it lives in `internal/messageproto`, the one converter every display
path already shares. A new `Options.ToolBodies` mode reaches
`contentBlockToProto`, and `AssembleForDisplay` and `Assemble` take the mode
from the handler. The decision lives in a new `stub.go`, a pure function of
(block, tool name, durable status, size) that is tested as a table:

| Block | Elided when | Sent instead |
|---|---|---|
| `TOOL_CALL` | the input is **over 1 KiB**, the tool is **not always-full**, and the call is **terminal** as displayed (`COMPLETED`/`FAILED`/`CANCELLED`, which includes an inherited in-flight call reported `CANCELLED`) | `input` = **skeleton** (below); `body_elided`, `body_size` |
| `TOOL_RESULT` | the content is over 1 KiB and the tool is not always-full | `content` = `PreviewToolResult` at a **1 KiB** budget (a JSON object stays valid with scalars intact, so the shell result keeps `exit_code`); `truncated` and `original_size` as today; `body_elided`, `body_size` |
| `THINKING`, `REDACTED_THINKING` | always (nothing renders them) | empty content; `body_elided`, `body_size` |
| everything else | never | unchanged |

**Always full** (inputs and results), whatever their size: every
`structuredResultTools` entry (the plan and task tools, `ask_user`,
`request_machine`), plus `draft_automation` (and `*__draft_automation`),
`generate_image`, `generate_video`, `start_run` and `get_run`. The
collapsed-fold cards, the tasks sidebar and the media rows read these, and
they are small. MCP names match on their last segment, as today.
**In-flight calls are always full**, because a pending approval renders the
input (`showRichContent`) and a running call has nothing to fetch yet.

**The input skeleton** is what lets every collapsed surface keep working
without a fetch. It keeps the TypeScript formatters (`formatToolParams`,
`toolActionVerb`, `deployOfShellCall`, `requestMachineReason`) as the single
source of presentation, rather than porting about 1,150 lines of
`toolFormatters.ts` to Go:

- Not a JSON object: head-cut to 256 B on a rune boundary.
- An object, walked recursively. Numbers, booleans and null stay. A string of
  up to 512 B stays whole. A longer one becomes its first 256 B plus `…`, and
  `command` keeps up to 4 KiB, so that `deployedEnvOf` still finds a
  `forge env deploy <env>` behind a long chain. `patch_text` keeps exactly its
  `*** (Begin|End) Patch` and `*** (Update|Add|Delete) File:` / `*** Move to:`
  lines, which is everything `formatPatchParams` reads. Arrays keep their
  first 200 elements, each walked, so `edits[].file_path` survives.
- The formatter fields this preserves are `file_path`, `path`, `pattern`,
  `glob`, `query`, `url`, `command`, `description`, `title`, `preset`,
  `subject`, `name`, `action`, `task_id`, `status`, `process_id`, `agent_id`
  and `edits[].file_path`, plus the first line of a `message`.

**The cross-language guard.** A table test writes a golden corpus,
`internal/messageproto/testdata/skeleton_corpus.json`: a real-shaped input for
each tool and its skeleton. A web vitest reads the same file and asserts that
the label, verb, file paths, deploy env and request-machine reason come out
the same from the skeleton as from the full input. If someone adds a
formatter that reads a key the skeleton cuts, that test fails rather than the
UI quietly drifting. Known gap: `formatPatchParams` falls back to
`${patchText.length} chars` for a header-less patch. Under a skeleton it
reads `inputSize` instead, a one-line client change made in the same PR.

**Replay and live** (`chat_stream.go`): `chatUpdateToProto` takes the mode
explicitly. **Live frames stay as they are today** (inputs whole, results at
8 KiB): they arrive one at a time, while someone watches, and in detailed mode
an edit's diff is expanded the moment it lands. **Replay frames under STUB**
get the result stub and the thinking elision, through
`PreviewMessageUpdate(raw, mode)`, but **keep tool-call inputs whole**. A
replayed assistant message carries the tool status from when it was *written*
(always pending, because the message is saved before the call runs), so
replay cannot tell a finished call from one still running without a status
read per batch. Replay is bounded by `maxChatReplayGap` (2,000 updates), so
this is a later optimisation, not a correctness gap. `GetChatUpdates` gets
the same mode. Nothing asks for it yet, and the Go SDK keeps FULL.

**`GetContentBlockBodies`** (`rpc_get_content_block_bodies.go`, scaffolded
with `reliant forge scaffold rpc chat GetContentBlockBodies`):

1. `requireChatOwner(chat_id)`.
2. Read the blocks by primary key, in one query
   (`store.ListContentBlocksByIDs`, `WHERE id = ANY($1)`), and their messages
   in one more. This avoids the per-block `GetMessage` loop that
   `chatToolResultBlock` has today.
3. A block is visible if its message is in `chat_id`. Otherwise its message's
   context window must be in `reachableContextWindows(chat_id)` and its chat
   must belong to the caller. A block of a HIDDEN message (a compaction
   summary) is not in the transcript, so it is not_found too. Every failure
   is `not_found`.
4. A result is converted with `PreviewToolResult` at 8 KiB (the expanded
   row's view), or whole when `full`. The authorization of `GetToolResult`
   moves into a shared helper both rpcs call. `GetToolResult` stays, for
   older clients.

**Kill switch.** An env var, `RELIANT_TRANSCRIPT_TOOL_BODY_STUBS=off`, makes
the server treat STUB as FULL. If a stub ships a regression, a config flip
puts everyone back on full payloads without a client release. Old and new
clients both keep working either way, because a client acts on the flags of
each block, not on the mode it asked for.

**Version skew is safe in every direction.** An old client never asks, so it
gets FULL. A new client talking to an old server asks, the field is ignored,
it receives no `body_elided` blocks, and it behaves as today. The SDK
defaults to FULL. During a rolling deploy, a body fetch can land on an old
pod and get `Unimplemented`. The client shows that row's "Couldn't load"
state with Retry; the next attempt reaches a new pod.

### 3. Client

**Opting in.** `chat-actions.listMessages` passes
`toolBodies: ToolBodyMode.STUB`. That covers the seed, scroll-back, thread
reads, `ChatSearch` (which reads only TEXT: a pure win) and `useFirstPrompt`.
`NodeDetailsPanel`'s `useGetMessage` passes it too, and the web's stream
passes it through the SDK option below. The chat-cache key does not change,
because the store's invariants below make a stub-mode list and a full list
interchangeable.

**Data model, all additive.** `ToolCallData` gains `inputElided?` and
`inputSize?`. The SDK's `ResolvedToolResult` and the web's `ToolResultData`
gain `elided?` and `block_id?`. `resolvedToolResultFromBlock` copies them.
`processMessage` copies `bodyElided` and `bodySize` from the call block.

**Store invariants (SDK reducers, `tool-results.ts`).**

- *A stub never erases a body the client holds.* In `mergeMessages`, both the
  upsert and the snapshot replace, an incoming block that is `body_elided`
  and has the same id as a held block that is not elided keeps the held
  block. This is the live → reload → resync path. A turn watched live holds
  full blocks. A reconnect's snapshot re-delivers them as stubs, and the rows
  the user has open must not blank out.
- *Fuller wins in the result index.* `foldToolResultMessage` is first write
  wins today, and `loadMessages` overwrites. It becomes: an elided entry never
  replaces a non-elided one, and a non-elided one replaces an elided one.
  Otherwise first write still wins, so a re-delivered duplicate is still
  ignored. The doc comment and `chatStore.toolResultRedeliveryLogging`'s
  expectations change with it.

**The body cache.** Bodies live in React Query, not in the store: one entry
per block, `['contentBlockBody', chatId, blockId, full]`, with
`staleTime: Infinity` (a finished call's body never changes) and `gcTime`
10 min. They are kept outside the message objects so that `processMessage`'s
reference-keyed memo stays valid, and so the store never holds megabytes of
bodies nobody opened.

- `hooks/content-block-bodies.ts` has `useContentBlockBody(chatId, blockId,
  enabled)` and `fetchContentBlockBodies(chatId, ids, {full})`. Behind them a
  **batcher** collects the ids requested within one frame
  (`requestAnimationFrame`, falling back to `setTimeout(0)`), sends chunks of
  100, re-queues `deferred` ids, and turns `not_found` into an error that is
  remembered (no retry storm).
- `GetContentBlockBodies` joins `api/rpcDeadline.ts` `FAST_READS`. It is a
  primary-key read with an owner check.
- `tool-result-queries.ts` (`GetToolResult`) stays for old servers.
  `fetchFullToolResult` prefers `GetContentBlockBodies({full})` when the call
  has a result block id.

**One render choke point: `ToolExecution`.** The header and the collapsed
state keep using `call.input`, which is the skeleton. That keeps the label,
verb, file links, duration, status, the deploy card and spawn routing, and
fetches nothing. When the row is **expanded** (by the user, by the collapse
defaults, or by `showRichContent`):

- If `call.inputElided` or `result.elided`, enable `useContentBlockBody` for
  that block (or both blocks).
- Until the body arrives, render a placeholder sized from `body_size` instead
  of `ToolContentArea`. **A renderer is never handed a skeleton as if it were
  the input.** Doing so would show a truncated diff as a real one, which is
  the worst failure mode here. `renderContext` takes the effective input and
  result, so the dozen renderers downstream do not change.
- Results keep their two levels. The row first shows the 8 KiB display body.
  If that body is `truncated`, it shows `TruncatedOutputNotice`, and "Show
  full output" fetches the full result as today.
- A body that cannot be fetched (not_found, error) shows the row's "Couldn't
  load this call's details" with Retry. The transcript around it is
  unaffected (recover gracefully).

**Fold prefetch.** When a `TurnWorkFold` (simplified mode) or a
`ToolExecutionGroup` opens, it prefetches the elided block ids of all its
rows in one batch, so rows that render expanded do not each start their own
request. In detailed mode, `fileWrite` is **expanded by default**
(`DEFAULT_TOOL_COLLAPSE_SETTINGS`), so on open the visible edit and write
rows fetch their inputs at once. Virtuoso mounts only the rows on screen,
which bounds this to one or two batched calls. This is open decision 1.

**Simplified mode needs nothing fetched while collapsed.** Here is what it
reads, checked against `turnWorkSummary.ts`, `TurnWorkFold.tsx` and
`ChatMessage.tsx`'s `work-surfaced` slice:

| It reads | Source under STUB |
|---|---|
| tool name, counts per category (`getToolCategory`) | `tool_name`, not elided |
| running/failed counts | `tool_call_status`, live state, `is_error`: not elided |
| "current step" line (`stepLabel` → `formatToolParams(...).summary`, `toolActionVerb`) | the skeleton; corpus-guarded |
| elapsed (`durableCompletedAt`), spawn status (`childWorkflowId`) | block timestamps and `child_workflow_id`, not elided |
| `ask_user`, `request_machine`, draft automation cards | always-full tools |
| pending approvals | in-flight, so always full |
| deploy cards (`deployOfToolCall`: `command` + `exit_code`) | skeleton `command` (4 KiB) + stub result (JSON with scalars kept) |
| generated images and videos | IMAGE blocks and attachments, never elided |
| blocked warning (`is_error` and `"blocked"` in content) | permission errors are under 1 KiB, so never elided |

**Copy, export, search, branch, SDK.**

- *Copy message* (`ChatMessage.handleCopy`): before formatting, it resolves
  every elided input and truncated result of the message with one
  `fetchContentBlockBodies(…, {full: true})`. The current per-call
  `fetchFullToolResult` loop folds into that one call.
- *Export* (`useChatActions.buildChatMarkdown`): the same, batched over the
  loaded messages. It already does this for `truncated` results, one RPC per
  result. It becomes 100 per call. Elided thinking stays out of the export,
  as it is out of the UI today.
- *Search*: "current chat" reads TEXT blocks only, so it is unaffected and
  faster. `SearchChats` is server-side and unaffected.
- *Branch*: server-side, so unaffected. A branch's inherited stubs resolve
  through the inheritance rule in `GetContentBlockBodies`.
- *SDK (`@reliantlabs/sdk`)*: `ChatSession` and `ChatStream` get an option
  `toolBodies?: "full" | "stub"`, **default `"full"`**. A third-party consumer
  of `reduceChat` (#2265) or `useChat` that renders inputs keeps getting what
  it renders. The SDK exports `fetchContentBlockBodies` and the new fields,
  and `buf.gen.sdk-ts.yaml` adds `reliant.v1.ChatService.GetContentBlockBodies`
  to `types`. This is an additive minor version. The Go SDK is unchanged:
  `proto/public-api.txt` keeps the new rpc withheld (it is listed only on
  `partial ChatService` lines that are exported).

### 4. The live → reload transition

A turn watched live arrives full: the live frames are unchanged, and an
in-flight call is never elided anyway. When the user reloads, or switches away
and back with a cold cache, the snapshot sends the turn's finished calls as
stubs, and expanded rows fetch their bodies. A reconnect that gets a snapshot
while the chat is held keeps the bodies it has (the "a stub never erases a
body" invariant). So the rule a reader can state in one line is: **the open,
live window is full; history is stubs; a body, once held, is kept.**

## Alternatives considered

- **Send a server-computed summary instead of a skeleton**
  (`ToolCallSummary{label, verb, file_paths, deploy_env}`). This would mean
  porting `toolFormatters.ts`, `toolAction.ts` and `deployOfShellCall` to Go,
  so two implementations of every label could drift, and every new formatter
  would need a proto change. Rejected. The skeleton plus the golden corpus
  keeps one implementation, the TypeScript one.
- **Lower the caps for everyone, with no opt-in.** An old client cannot know
  an input was cut, so it would render a cut diff as the real one. That
  breaks v1 and is silently wrong. Rejected.
- **Fetch by tool_call_id with `GetToolResult` plus a new `GetToolInput`.**
  It is ambiguous where providers repeat ids, needs N calls with no
  batching, and has no path for thinking. Rejected for the batched,
  block-keyed rpc.
- **An outline RPC plus per-message hydration** (messages without blocks, then
  blocks on demand). This is a much larger protocol and store change. The
  stub sits inside the existing `ContentBlock`, so every existing read path
  and reducer keeps its shape.
- **Stub live frames as well.** It is more uniform, but every live edit in
  detailed mode would cost a round trip, and live bytes are not the problem
  (they arrive spread over the turn). This is open decision 2.

## Plan: four PRs

PR 0 and PR 1 can run in parallel. PR 2 needs PR 1's generated types. PR 3
needs PR 1 and PR 2. PR 0 should land a few days before PR 3, to give a
baseline.

### PR 0: measure first (`perf(chat): record transcript byte share on every read`)

- Add attributes to the `ListMessages` handler's span, the
  `chat_snapshot.assemble` span and the replay batch span:
  `transcript.messages`, `transcript.bytes` (`proto.Size` of what is sent),
  and `transcript.{text,tool_input,tool_result,thinking}_bytes` (string
  lengths summed in `Assemble`, already in hand).
- Tests: a `messageproto` unit test for the byte accounting, plus a handler
  test asserting that the attributes are set (a tracetest recorder).
  `task test:short -- ./internal/messageproto/... ./internal/handlers/chat/... ./internal/handlers/streaming/...`
- This gives the real byte share on prod traffic in ClickStack within days,
  which replaces this document's estimate.

### PR 1: protocol + server (`feat(chat): ToolBodyMode STUB on transcript reads, GetContentBlockBodies`)

1. Proto edits as in §1. Then `task generate:forge`, which runs buf over
   `buf.gen.yaml` (Go, `frontends/web/src/gen`, the generated
   `chat-service-hooks_gen.ts` and `mocks/scenario-rpcs_gen.ts`) and the
   `sdk-ts-gen` and `sdk-go-gen` hooks, regenerating
   `internal/handlers/mocks`. Then `reliant forge scaffold rpc chat
   GetContentBlockBodies`, `task generate:check` (clean), and
   `buf breaking --against '.git#branch=origin/main'`. Update
   `buf.gen.sdk-ts.yaml` `types` and run `task generate:forge` again.
   `reliant forge lint --scope internal/handlers/chat --scope internal/messageproto --scope proto/reliant`.
   Run `task generate:docs` if `generate:check` names the API reference.
2. `internal/messageproto/stub.go`: the policy, the skeleton,
   `PreviewMessageUpdate(raw, mode)`, and the mode threaded through
   `Options`, `AssembleForDisplay` and `Assemble`.
3. Thread the mode through ListMessages, GetMessage, `buildChatSnapshot`, the
   replay `chatUpdateToProto`, and GetChatUpdates. Live stays FULL.
4. `GetContentBlockBodies`: `store.ListContentBlocksByIDs` and the repo
   facade, a shared visibility helper (with `GetToolResult` moved onto it),
   the caps and deferral, and the kill-switch env.
5. Tests:
   - `stub_test.go`, table-driven: each row of the §2 table; always-full
     tools including MCP prefixes; in-flight and inherited-cancelled calls;
     the 1 KiB boundary; a shell JSON stub keeping `exit_code`; the skeleton
     rules (rune boundary, `command`, `patch_text` headers, arrays); and the
     stored block left untouched.
   - The golden corpus generator: a test that fails if
     `testdata/skeleton_corpus.json` is stale, with `-update` to rewrite it.
   - Handlers, DB-backed (pgtest):
     - ListMessages UNSPECIFIED returns bytes identical to today (the v1
       guard); STUB elides; a thread read; scroll-back.
     - Snapshot STUB; replay STUB keeps inputs; live frames are unchanged.
     - GetContentBlockBodies: own chat; branch-inherited; another user's
       block is not_found; HIDDEN is not_found; more than 100 ids is
       INVALID_ARGUMENT; the 4 MiB deferral, with at least one body returned;
       `full`.
     - The kill switch.
   - Run `task test:short -- ./internal/messageproto/... ./internal/handlers/chat/... ./internal/handlers/streaming/... ./internal/handlers/message/... ./internal/store/...`,
     then `task test:affected`. Run `task test -- <same pkgs>` once at the
     end.
6. Behaviour change: none until a client opts in.

### PR 2: SDK (`feat(sdk): opt-in tool body stubs and fetchContentBlockBodies`)

- `tool-results.ts`: `elided` and `block_id`, and the fuller-wins fold.
  `reducers.ts` `mergeMessages`: keep held bodies. `chat-stream.ts` and
  `chat-session.ts`: the `toolBodies` option (default `"full"`), plus
  `fetchContentBlockBodies`. README section.
- Tests (`sdk/ts`, vitest, scoped): `chat-reducer.test.ts` (a snapshot of
  stubs over held full blocks keeps them; a stub never displaces an indexed
  full result; full upgrades a stub); `chat-session.test.ts` (the option
  reaches `ListMessages` and the stream request; default FULL).

### PR 3: web (`feat(web): open chats with tool bodies fetched on expand`)

- The opt-ins of §3, `hooks/content-block-bodies.ts` (batcher + cache), the
  `ToolExecution` choke point, the fold prefetch, copy and export over one
  batch, `formatPatchParams`'s `inputSize`, `FAST_READS`.
- Client telemetry. A `tool_body.expand` interaction (forge-web-runtime
  `interaction()`, like `chat.open`) from click to body painted, with
  `blocks`, `bytes`, `cache: hit|miss`, `deferred` and `outcome`. A
  `chat.tool_bodies` attribute (`stub|full`) on `chat.open`, so that open
  times can be compared before and after.
- Tests (scoped vitest):
  - The golden corpus test: `toolFormatters.skeleton.test.ts` reads
    `internal/messageproto/testdata/skeleton_corpus.json`.
  - ToolExecution: collapsed with stubs makes no fetch; expanding fetches
    once; a placeholder (never a skeleton) is rendered; an error state with
    Retry; the two levels for a truncated display body.
  - `TurnWorkFold`: the summary from stubs matches the summary from full
    input; opening it prefetches one batch.
  - The batcher: coalescing, chunks of 100, deferred re-queue, not_found
    remembered.
  - Copy and export resolve elided inputs, with no stub in the clipboard.
  - The live → snapshot transition keeps bodies.
  - `ChatSearch` and `chat-actions` request STUB.
- Run `task test:affected LANES=web` (never the unscoped vitest).

### Later (not in this change)

- Replay inputs: a status read per replay batch, so terminal calls' inputs
  stub there too.
- DB read: a STUB read still detoasts every body to build the skeleton and
  the stub. A store variant could read `octet_length` and skip the bodies of
  blocks it will drop outright (thinking, and results whose tool needs no
  JSON preview).
- `chatStore.loadMessages` (an unbounded ListMessages) has no callers outside
  tests. Delete it.

## Telemetry and success criteria

- Server (PR 0, then PR 1's `transcript.tool_bodies`, `transcript.blocks_elided`
  and `transcript.bytes_elided`): snapshot and ListMessages bytes, p50 and p95,
  STUB against FULL.
- Client: `chat.open` p50 and p95 split by `chat.tool_bodies`;
  `tool_body.expand` p95 (target under 300 ms on a cache miss); the body fetch
  error and not_found rate (target about 0; a nonzero not_found rate means the
  visibility rule and the transcript disagree, which is a bug).
- Watch layout shift in Virtuoso when a placeholder resolves to a diff. The
  `body_size` height estimate is there to bound it.

## Risks

- **A skeleton shown as a body.** Guarded by the single choke point in
  ToolExecution, by a test that renders every renderer with an elided call
  and asserts the placeholder, and by `truncated` on every elided result
  (copy and export fetch).
- **Formatter drift.** Guarded by the golden corpus test.
- **Detailed mode's expanded diffs flash a placeholder on open.** Bounded by
  virtualization and batching. See decision 1.
- **Recovery.** Roll forward with the kill switch (server config, immediate).
  Clients degrade to FULL on their own. No schema change, so nothing to
  migrate back.

## Decisions for the owner

1. **Detailed mode's default-expanded file diffs** (`fileWrite: false`).
   (a) Fetch on mount, batched and bounded by virtualization: most of the
   savings, plus a brief placeholder on open (recommended). (b) Never elide
   `edit`/`write`/`patch` inputs: no flash, but it keeps what are likely the
   largest inputs. (c) Collapse file writes by default: a UX change.
2. **Live frames.** Keep them full (recommended: there is no fetch while
   watching, and live bytes are not the problem), or stub live frames too
   (uniform, but a round trip for every edit you watch).
3. **Thinking content.** Elide `THINKING` and `REDACTED_THINKING` in STUB
   mode. Nothing renders them, so the cost is zero. It is beyond "tool
   bodies", so it is flagged here. Recommended: yes.
4. **Kill switch.** Ship `RELIANT_TRANSCRIPT_TOOL_BODY_STUBS` as a server env
   (recommended), or rely on fix-forward releases only.
5. **The new rpc in the Go SDK's public API.** Withhold it for now
   (recommended: the Go SDK reads updates, not transcripts), or export it in
   `proto/public-api.txt` now.

Tunables, not decisions: the 1 KiB elision threshold, the 1 KiB result stub,
the 512/256 B skeleton strings, `command`'s 4 KiB, the 100-id and 4 MiB caps.

## Appendix: the byte-share queries (run READ ONLY against localhost:5434, db `reliant`)

```sql
-- What a transcript read sends, by block type, over the 20 largest chats:
-- stored bytes, what goes on the wire today (#1680's 8 KiB result preview),
-- and roughly what STUB would send (bodies over 1 KiB shrink to ~1 KiB for
-- results and ~300 B for input skeletons; thinking to nothing). Ignores the
-- always-full tools and in-flight calls, so wire_stub is slightly low.
WITH big AS (
  SELECT m.chat_id
  FROM messages m
  JOIN message_content_blocks b ON b.message_id = m.id
  GROUP BY m.chat_id
  ORDER BY sum(coalesce(octet_length(b.content), 0) + coalesce(octet_length(b.tool_input), 0)) DESC
  LIMIT 20
), blocks AS (
  SELECT b.block_type,
         coalesce(octet_length(b.content), 0)    AS content_bytes,
         coalesce(octet_length(b.tool_input), 0) AS input_bytes
  FROM big
  JOIN messages m ON m.chat_id = big.chat_id
  JOIN message_content_blocks b ON b.message_id = m.id
), sized AS (
  SELECT block_type,
         content_bytes + input_bytes AS stored,
         CASE WHEN block_type = 3 THEN least(content_bytes, 8192) ELSE content_bytes END + input_bytes AS wire_today,
         CASE WHEN block_type = 3 THEN least(content_bytes, 1024)
              WHEN block_type IN (5, 8) THEN 0
              ELSE content_bytes END
         + CASE WHEN block_type = 2 AND input_bytes > 1024 THEN 300 ELSE input_bytes END AS wire_stub
  FROM blocks
)
SELECT CASE block_type WHEN 1 THEN 'text' WHEN 2 THEN 'tool_call (input)' WHEN 3 THEN 'tool_result'
                       WHEN 4 THEN 'image' WHEN 5 THEN 'thinking' WHEN 6 THEN 'file_ref'
                       WHEN 7 THEN 'document' WHEN 8 THEN 'redacted_thinking' ELSE block_type::text END AS kind,
       count(*) AS blocks,
       pg_size_pretty(sum(stored)) AS stored,
       round(100.0 * sum(wire_today) / sum(sum(wire_today)) OVER (), 1) AS pct_of_wire_today,
       pg_size_pretty(sum(wire_today)) AS wire_today,
       pg_size_pretty(sum(wire_stub)) AS wire_stub
FROM sized
GROUP BY block_type
ORDER BY sum(wire_today) DESC;

-- Which tools the input bytes belong to (where the skeleton wins most).
SELECT b.tool_name,
       count(*) AS calls,
       pg_size_pretty(sum(octet_length(b.tool_input))) AS input_bytes,
       pg_size_pretty(percentile_cont(0.95) WITHIN GROUP (ORDER BY octet_length(b.tool_input))::bigint) AS p95_input
FROM message_content_blocks b
WHERE b.block_type = 2 AND b.tool_input IS NOT NULL
GROUP BY b.tool_name
ORDER BY sum(octet_length(b.tool_input)) DESC
LIMIT 15;
```
