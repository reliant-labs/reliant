# Frozen: an ask_user wait that ignored its thread's input (recorded before ask-user-yields-to-thread-input)

`ask_user_ignores_thread_input.json` was recorded on `4e0b5b44` (main before the
change). A `builtin://agent` run calls `ask_user` and parks on the question
(`QuestionCreate`, then the 24h timer). While it waits, the user messages the
asking thread through the production `SendAgentMessage` handler (its
`thread_wake` doorbell, event 58) and interrupts it (`interrupt_thread`, event
62). The old wait listened only for the answer and the timer, so it kept
waiting through both; the answer (`signal.question.<id>`, event 66) cancels the
timer, and the next `CallLLM` reads the queued message and completes.

That is the shape of every run that was parked in `ask_user` at deploy time —
prod chat adcee226 among them, with the doorbells and interrupts it had
already been sent.

After the change the wait also ends when the user messages the asking thread
(a doorbell with `FromUser`, which every chat-handler doorbell now sets) or
interrupts it. What keeps this replaying: `askUserYieldChangeID`
(`ask-user-yields-to-thread-input`, `internal/workflow/runtime/ask_user_yield.go`),
asked once per observation of new input as
`ask-user-yields-to-thread-input/<question id>/<n>`. Replaying this history,
the doorbell predates `FromUser` and is not counted at all; the interrupt is
observation 1, which has no marker, so it answers `DefaultVersion` and the wait
keeps going, exactly as it did. A run like this one that is still open takes
the new path at its first observation made live, on the new build.

Recorded with a scratch copy of `TestGenerateFixture_AskUserIgnoresThreadInput`
(`TestGenerateFixture_AskUserYieldsToMessage` in `generate_ask_user_gen_test.go`,
minus the parts that only exist after the change, plus `ResolveQuestion` after
a two-second pause). Verified to fail replay if the yield is not versioned.

Retire this set only together with the gate, once no run parked in `ask_user`
before the gate's deploy can still be open.
