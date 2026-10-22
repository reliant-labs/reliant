# Async ask_user — asking without blocking

Owner request: "an async option to the ask/question tool that can go to the
chat. This will not block the chat, and maybe show in the chat timeline to be
answered (but not in the chat inbox), but otherwise continue the chat. Then it
will show in the user's inbox."

## Behaviour

- `ask_user` takes `async: true`. The tool returns at once ("posted, keep
  working, the answer arrives later as a message from the user"); the agent's
  loop continues. Unattended runs resolve it like a blocking ask (no human).
- The question is a `questions` row with `async = true`
  (`db/migrations/20261022020559_async_questions.up.sql`). Its metadata JSON
  also carries `"async": true`.
- It is **never a gate**: excluded from `pending_input_gates` (so never
  AWAITING_INPUT in `chats_with_activity`, and its thread still counts in
  `working_threads`), from `ListPendingQuestions` /
  `ListAnswerablePendingQuestionsByChat` / `ListPendingQuestionsByChat` /
  `PendingQuestionsByThread` (composer, background-work pill, reconciler,
  terminate, failed-run answer-as-resume, spawn_status), and from the
  run-event blocker count. QuestionCreate does not mark the chat unread and
  does not emit a run "blocked" event for it.
- It is **not closed when its thread ends** (#1250's closes skip it). It stays
  pending until answered (or hidden via the inbox's dismissal).
- It is listed in the user's **inbox** whatever state its run is in (the
  blocking-question liveness filters do not apply), as a question item with
  `InboxQuestion.async = true`. It is not counted in `blocking_count`; it sets
  `has_informational`.
- The chat **timeline** renders an async ask_user tool call as an answerable
  card. Data: `QuestionService.ListAsyncQuestions(chat_id)` (every async
  question in the chat, pending and resolved, oldest first; `QuestionInfo`
  gained `async`, `tool_call_id`, `response_data`) plus stream events
  (`QuestionUpdate` JSON gained `async` and `tool_call_id`).
- Both the card and the inbox answer through **`ResolveQuestion`**.

## Answer delivery (ResolveQuestion on an async question)

The answer is delivered like a message the user sends to the asking thread,
through the chat service's own send paths (no parallel implementation):

1. Body: the question(s) and answer(s), formatted for the model.
2. Asking thread is the chat's main thread → `SendMessage` (target main).
   That is the composer's path: queued to the mailbox while the run executes,
   resumes a paused or failed run, starts a run when the chat's run ended.
3. Asking thread is a sub-agent → `SendAgentMessage` to it: queued + FromUser
   doorbell (a thread parked on a blocking ask_user yields to it, #1241), a
   paused chat is resumed first, a sub-agent that stopped on an error is
   resumed. If it refuses (the sub-agent has finished, or its parent has), the
   answer goes to the main thread via `SendMessage`, framed as "a sub-agent
   asked this and has since finished; resume it with spawn(agent_id=…) if it
   still matters".
4. Only after a delivery succeeds is the question resolved (`response_data`
   kept) and a `resolved` update emitted. A refused or failed delivery returns
   an error and leaves the question pending, so an answer is never lost; the
   user can retry. Delivery is idempotent: the client message id is a UUIDv5 of
   (question id, target thread, body), so a retry of the same answer never
   queues it twice. Not signalled to the workflow: nothing waits on it.

## Determinism

`executeAskUserInline` reads `async` from the tool input (history data) and
only then calls `workflow.GetVersion("ask-user-async/<tool_call_id>", Default,
1)`. Existing histories never had an async call, so they never reach the
GetVersion; a hypothetical old call with `async: true` replays as DefaultVersion
→ the blocking path. The async path is QuestionCreate (same activity) and
return — no timer, no signal wait.

## Alternatives considered

- **A separate tool (`post_question`)** — two tools for one concept; the model
  already knows ask_user. Rejected.
- **Server-side activity tool with no workflow involvement** — ask_user is
  intercepted in the workflow anyway (needs workflow/thread ids, unattended
  handling); an inline branch is smaller than a new execution path.
- **Answer as a `signal.question` the workflow listens for in the background**
  — a background listener per async question must outlive the thread's loop
  and the run (continue-as-new, completion); the mailbox already has those
  semantics and survives everything. Rejected.
- **Client calls ResolveQuestion then SendAgentMessage** — two writes, not
  atomic, and the inbox would need the same logic. Rejected for one RPC.
