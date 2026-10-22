# Frozen: an ask_user call with no flag, recorded when that meant blocking (before ask_user defaulted to async)

`ask_user_yields_to_message.json` is the current-set fixture as it stood on
`cbc5221a` (recorded on `c98caab4`), before ask_user became async by default.
A `builtin://agent` run calls `ask_user` with no `async` or `blocking` field —
blocking, then — and parks on the question (`QuestionCreate`, then the 24h
timer). The user messages the asking thread through the production
`SendAgentMessage` handler; its doorbell ends the wait
(`ask-user-yields-to-thread-input/<question>/1`), the question is resolved
unanswered (`QuestionResolve`), and the next `CallLLM` reads the message.

It holds no `ask-user-async/<tool call>` marker: the opt-in build asked that
version only for an input with `async: true`. That is the shape of every
ask_user call recorded before the change — every run parked in a blocking
ask_user at deploy time among them.

After the change a call without a flag posts its question and returns, with no
timer. What keeps this replaying: `askUserIsAsync`
(`internal/workflow/runtime/ask_user_async.go`) asks
`ask-user-async/<tool call id>` for every attended ask_user call, with
`askUserAsyncByDefault` (2) as the maximum. Replaying this history it finds no
marker and answers `DefaultVersion`, which blocks. A history the opt-in build
recorded with `async: true` answers `askUserAsyncOptIn` (1) and stays async;
a new call records 2 and is async unless its input says `blocking: true`.

Verified to fail replay if the default is not versioned: with `askUserIsAsync`
reduced to `!blocking`, `TestReplayFixtures` fails this set (and
`2026-10-21-ask-user-ignores-thread-input`) with TMPRL1100, "a matching Timer
command was expected in history event position 57".

Retire this set only together with the gate, once no run that recorded an
ask_user call before the gate's deploy can still be open.
