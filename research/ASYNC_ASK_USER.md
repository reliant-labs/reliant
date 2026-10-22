# Async ask_user — asking without blocking

Owner request: "an async option to the ask/question tool that can go to the
chat. This will not block the chat, and maybe show in the chat timeline to be
answered (but not in the chat inbox), but otherwise continue the chat. Then it
will show in the user's inbox."

## Behaviour

- `ask_user` is **async by default**: the tool returns at once ("posted, keep
  working, the answer arrives later as a message from the user"); the agent's
  loop continues. `blocking: true` is the explicit opt-in to stop and wait, for
  when the agent truly cannot make progress without the answer. (The option
  shipped first as an opt-in `async: true`; the default flipped, and the schema
  now offers only `blocking`.) Unattended runs resolve either kind the same way
  (no human).
- `ask_user` is in `tag:coding:default` and `tag:coding:plan`, so the coding
  agent has it preloaded, without `load_tool`; `list_pending_questions` and
  `withdraw_question` follow it in structurally (below).
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
3. Asking thread is a sub-agent → `SendAgentMessage` to it: queued + doorbell,
   a paused chat is resumed first, a sub-agent that stopped on an error is
   resumed. If it refuses (the sub-agent has finished, or its parent has), the
   answer goes to the main thread via `SendMessage`, framed as "a sub-agent
   asked this and has since finished; resume it with spawn(agent_id=…) if it
   still matters".
   The doorbell is never the user's (`threadwake.WithoutQuestionYield` →
   `FromUser: false`, as for spawn_send): the answer is to THIS question, so a
   blocking ask_user its thread may be parked on must not yield to it (#1241's
   yield is for the user's own new input) and close unanswered.
   **Asking thread parked on a blocking question B** (whatever its run's
   state): the answer is only queued in its mailbox — no doorbell, no resume,
   no run started, never SendMessage's answer-as-resume of B. B stays pending;
   the thread reads the answer at the call_llm after the user answers B. That
   mailbox is not swept as undelivered even if the thread fails meanwhile
   (`MarkQueuedAgentMessagesUndeliveredForThread` /
   `ListThreadsWithOrphanedAgentMessages` skip a thread with a pending blocking
   question: that question resumes it).
4. Only after a delivery succeeds is the question resolved (`response_data`
   kept) and a `resolved` update emitted. A refused or failed delivery returns
   an error and leaves the question pending, so an answer is never lost; the
   user can retry. Delivery is idempotent: the client message id is a UUIDv5 of
   (question id, target thread, body), so a retry of the same answer never
   queues it twice. Not signalled to the workflow: nothing waits on it.

## Determinism

`executeAskUserInline` decides the path in `askUserIsAsync`, after the
unattended short-circuit and before `QuestionCreate`, with
`workflow.GetVersion("ask-user-async/<tool_call_id>", Default, 2)` — asked for
every attended call. The version names the rule the history recorded:

| Version | Recorded by | Async when |
|---|---|---|
| `DefaultVersion` (no marker) | builds before the async option, and the opt-in build for a call without `async: true` | never: the call blocked |
| 1 `askUserAsyncOptIn` | the opt-in build (#1262), only for `async: true` | input `async: true` |
| 2 `askUserAsyncByDefault` | every call now | input lacks `blocking: true` |

So an in-flight history whose ask_user omitted the flag replays the blocking
wait it took (`frozen/2026-10-22-ask-user-blocks-by-default`, which fails
replay with TMPRL1100 if the default is unversioned). The async path is
QuestionCreate (same activity) and return — no timer, no signal wait
(`fixtures/ask_user_async_by_default.json`).

The web client classifies a tool call from its input (`isAsyncAskUserInput`:
async unless `blocking: true`). A call recorded before the flip reads as async
there too; its `AsyncQuestionCard` renders nothing once the call has a result
and no async question row matches it.

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

## Seeing and withdrawing pending questions

An agent that keeps going needs to see what it is still waiting on, and to
retract a question its default has made moot, or the user's inbox fills with
stale ones. Two tools, handed to any agent that can ask or that has
sub-agents (`capabilities.go`, structural tools) — so they are never deferred
behind `load_tool` for an agent that has `ask_user`, and are not offered to one
that cannot ask and owns no askers:

- `list_pending_questions` — no parameters. Lists the pending questions,
  async and blocking, of the caller's thread and every sub-agent below it,
  recursively, never a sibling's. Each entry: question_id, agent_id and the
  sub-agent's title (spawn_status's names), async or blocking, the questions
  with their options, created_at and how long it has waited.
- `withdraw_question(question_id, reason)` — a separate tool, like
  spawn_status/spawn_send/spawn_stop. Async questions only, within the same
  subtree. Withdrawing resolves the row with `questions.withdrawn_reason`
  (no answer), so it leaves the inbox and the timeline card shows
  "Withdrawn: <reason>". A blocking question is refused: its agent is parked
  on it, and only the user's answer or that agent ending closes it. A
  question outside the subtree, or already answered, gets one "not found"
  answer, so nothing leaks about a sibling's questions.

The subtree is walked over `threads.parent_thread_id`
(`store.threadSubtreeCTE`), not the `workflows.parent_id` walk the terminal
cascade uses: a thread is the agent, a workflows row is one run of it (a
resumed sub-agent gets a new row on the same thread), and questions name
their thread.

Not included: "async questions answered since the thread last drained its
mailbox". The answer message already names its question_id
("Answer to the question you asked without waiting (ask_user, question_id:
…)"), which connects an answer to its question without a per-thread drain
watermark to maintain.
