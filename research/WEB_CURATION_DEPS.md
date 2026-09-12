# What reliant-web depends on from engine curation

Research-only. Question: if engine injection/curation became opt-in and were
turned OFF for a run, what in the web UI would break?

## Verdict

**Nothing in the web UI breaks structurally.** Every always-on injection the
engine performs is *prompt-side only* — it is never persisted as a DB message,
never streamed to the client, and therefore never rendered. The UI's transcript
is built exclusively from persisted `messages` rows, and the injected material
is assembled fresh in-memory at prompt-assembly time.

The load-bearing line: `internal/workflow/runtime/activities/handlers/call_llm.go:1126-1148`
builds a `prefix []message.Message` carrying memory-file content and per-repo
`reliant.md`, prepends it to `history`, and the comment states explicitly
"These are NOT saved to DB — re-injected each turn". Same for the persona /
working-directory / multi-repo / skills-announcement system prompt built in
`getSystemPrompts` (`call_llm.go:2114-2190`) — it goes to
`driver.StreamResponse(streamCtx, systemPrompts, history, availableTools)`
(`call_llm.go:1345`) and nowhere else.

What degrades is **agent behavior quality**, not UI correctness: with no working
directory in the prompt the model invents `/path/to/project` paths
(`call_llm.go:2131-2140` documents eighteen measured File-not-found errors), and
with no shell-platform description (`internal/llm/tools/shell_platform.go:11-30`)
a Windows daemon gets bash syntax. Those are product-quality regressions an API
user opting into raw mode would be accepting knowingly.

## Q1 — Does the UI render injected content?

No.

- **System-role messages are not injected.** The only `MessageRole.SYSTEM`
  messages the UI ever renders are compaction summaries, matched by a *text
  prefix*: `isCompactionMessage` requires `role === SYSTEM` and the text to
  start with `"This session is being continued from a previous conversation"`
  (`web/src/components/Chat/CompactionMessage.tsx:15,28-32`). That string is
  produced at `internal/workflow/runtime/activities/handlers/compact.go:137`.
- **Hidden messages are filtered twice.** Server-side, `display_style = HIDDEN`
  rows are dropped before the proto transcript is built
  (`internal/grpc/services/proto_converters.go:301-305`). Client-side, the
  timeline skips them again: `if (msg.displayStyle === DisplayStyle.HIDDEN) continue;`
  (`web/src/components/Chat/thread-views/InterleavedTimeline.tsx:793`).
- Messages with a non-hidden `displayStyle` route to `SystemNotificationMessage`
  (`InterleavedTimeline.tsx:1655`), but those are *workflow-authored*
  (`save_message` with `display_style`, `proto/reliant/v1/workflow_v2.proto:143`),
  not engine-injected.

Strong signal: injection is already architecturally invisible to the client.

## Q2 — Does the UI depend on message SHAPES injection produces?

Only one, and it is not an injection the plan touches:

- `isCompactionMessage`'s prefix match (`CompactionMessage.tsx:15`). A raw run
  that never compacts simply never produces that message; the branch is inert.
- No first-message-is-X invariant. `InterleavedTimeline.tsx:831` and `:988` look
  for the first `MessageRole.USER` item purely for scroll/pin anchoring; absence
  degrades to no pinned header, not an error.
- `InterleavedTimeline.tsx:796-814` already defends against assistant messages
  with no visible content by skipping them (to avoid Virtuoso zero-height
  warnings), so a sparser raw stream is tolerated by construction.

## Q3 — What does the UI send?

Client-supplied only, and it is a short list. `chatGrpc.sendMessage`
(`web/src/api/chat-grpc.ts:390-416`) attaches exactly: `chatId`, `messages`
(role/content/displayStyle), `attachments` (attachment ID strings),
`workflow`, `mode`, `temperature`, `maxTokens`, `workflowParams`,
`targetThread`, `selectedPresets`, `discuss`. `createChat` is the same set plus
`projectId`, `worktreeId`, `title` (`chat-grpc.ts:256-268`).

There is **no** editor context, current file, cursor position, selected-files
set, or terminal state in the request. Everything the model learns about the
workspace today arrives through server-side injection or through tool calls.
That makes the client/server split clean: nothing the UI sends would be lost by
disabling injection.

## Q4 — Workflow params / presets

The UI always names the workflow explicitly — `const effectiveWorkflow = workflow ?? DEFAULT_WORKFLOW`
with `DEFAULT_WORKFLOW = "builtin://agent"`
(`web/src/store/chatStore.ts:1095`, `web/src/store/preferencesStore.ts:10`), and
the comment there says it does so because an empty string previously resolved
badly server-side.

It does **not** send `mode`, `tools`, `model`, or `skills` unless the user
changed them. Those come from server-side defaulting:
`buildWorkflowInputs` loads the workflow's declared inputs and applies their
defaults (`internal/grpc/services/chat_workflow.go:151-155`, via
`loadWorkflowInputsForBuild` at `:209`), then normalizes string model values to
`{id}` objects (`chat_workflow.go:161-163`). The defaults themselves are
declared in the workflow YAML — e.g. `internal/workflow/builtin/agent.yaml`
declares `mode: auto`, `tools: ["tag:default"]`, `model.tags: ["flagship"]`,
`system_prompt: ""`.

**This is declared defaulting, not curation.** An API user posting the same
workflow gets the identical treatment. The one thing to preserve is that
`ApplyDefaults` keeps running in raw mode — it is workflow-declared, so it is on
the correct side of the line.

`project_path` is injected into inputs unconditionally
(`chat_workflow.go:168-170`); worth a decision, since spawned workflows read it.

## Q5 — Would an empty system prompt break the UI?

No UI code reads the system prompt or depends on the persona. Markdown is
rendered generically (`web/src/components/Chat/MarkdownRenderer.tsx`); there is
no citation parser, no narration parser, no expectation of a tone.

The only *cosmetic* coupling is `file_path:line` references being clickable —
the UI turns them into links, and the persona prompt is what tells the model to
emit them. Raw runs lose the links, not the rendering.

## Q6 — Streaming / event contract

The UI needs no curation-only event. Enumerated from
`InterleavedTimeline.tsx:1594-1670`, it renders: user messages, assistant
messages, tool calls/results via `tool-renderers/`, `displayStyle`
notifications, run-step outputs, and compaction. Thinking blocks are a normal
content-block type (`CONTENT_BLOCK_TYPE_THINKING = 5`,
`proto/reliant/v1/chat.proto:93`) emitted by the model, not by curation.

A raw run produces a strict subset: user → assistant → tool → assistant. Every
curation-specific renderer (compaction, hidden-skip) is a conditional branch that
simply never fires.

## Q7 — Electron vs browser

No difference in any of the above. The message send path, the timeline, and the
proto converters contain no `isElectron` branch — `isElectron` appears only in
auth (`web/src/lib/supabase.ts:14`), external-link opening
(`web/src/lib/open-link.ts:39`), and protocol handling
(`web/src/lib/protocol.ts:16`). The renderer is treated as a same-origin browser
tab throughout.

## Which injections the UI genuinely needs vs merely tolerates

| Injection | Site | UI need |
|---|---|---|
| Persona / working-dir / multi-repo system prompt | `call_llm.go:2114-2190` | **None.** Safe to make opt-in. |
| Memory files (`reliant.md`, per-repo) | `call_llm.go:1131-1145`, `formatStoredMemories` `:2845` | **None** — ephemeral, never persisted. |
| Skills announcement | `internal/llm/tools/skill.go:551-563` via `call_llm.go:2178` | None. |
| Preloaded skill seed turn | `call_llm.go:1173-1178`, `preloadedSkillsPreamble` `:2917` | None; and it is already workflow-declared via `args.skills`. |
| Skill-suggestion `<system-reminder>` | `call_llm.go:2806-2821` | None. |
| Temporal-retry `<system-reminder>` | `call_llm.go:2727-2739` | None (reliability aid, keep on regardless — it is not curation). |
| Shell platform description | `internal/llm/tools/shell_platform.go` | None for UI; **correctness** risk for Windows daemons if removed. Recommend keeping it always-on — it describes the executor, not the persona. |
| Workflow input defaults (`mode`/`tools`/`model`) | `chat_workflow.go:151-163` | **Keep always-on.** Declared by the workflow, so it is not curation. |
| `project_path` input | `chat_workflow.go:168-170` | Needed by spawn; decide deliberately. |
| Hidden-message filtering | `proto_converters.go:301`, `InterleavedTimeline.tsx:793` | Keep — it is transcript hygiene, independent of injection. |

## Recommendation

Draw the opt-in boundary around the *prompt-assembly* injections (persona,
memory, skills announcement, skill suggestions) and leave the *executor-describing*
and *workflow-declared* pieces (shell platform description, `ApplyDefaults`,
retry hint) always-on. Under that split the web app needs no change at all.
