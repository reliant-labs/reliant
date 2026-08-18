# Queueing: the whole design

Status: design settled. Supersedes the mailbox sections of
`specs/thread-interrupt.md`.

## Two things that are NOT the same

I conflated these, and that is the root of the mess.

**Queueing** — a message arrives for a thread. Where does it go, who delivers it,
and how does the loop know to keep going?

**Pausing/resuming** — a run stopped. How does it start again from where it was?

They meet at exactly one point: a resumed run eventually calls `call_llm`, and
`call_llm` sweeps the mailbox like it always does. Nothing else about queueing
cares that a pause happened, and nothing about resume needs to know a mailbox
exists.

## Queueing, complete

**One writer.** `SendAgentMessage` inserts into `agent_messages` at
`status=queued`. That is the only way a message enters the mailbox.

**One deliverer.** `call_llm` sweeps the mailbox into thread history immediately
before it reads that history. That is the ONLY delivery point. It works for every
workflow shape (loop or not), and it is where the tool-pairing invariant is
already guaranteed, because the history is about to go to a provider.

**One continuation rule.** The agent loop keeps looping while the mailbox is
non-empty. `call_llm` reports `pending_inbox` after streaming; the loop's `while`
ORs it in. So a message that arrives during the final turn earns another turn,
and that turn's sweep delivers it.

That is the entire design. Three sentences, one mechanism each.

### What this deletes

`absorbQueuedMailbox` (`chat_send.go:118`) is a SECOND deliverer and must go. It
claims the mailbox from the gRPC handler and writes the rows into the transcript
itself, which:

- duplicates delivery logic that already exists in one correct place,
- writes messages without the envelope framing `call_llm`'s drain applies, and
- exists only to paper over "an idle run never reaches `call_llm`" — which is a
  RESUME problem, not a queueing problem, and is fixed in the resume section
  below.

Delete it and every call site. `SendMessage` saves the new user message and
starts/resumes the run; it does not touch the mailbox.

### What this makes obvious

**Interrupt is not a delivery mechanism.** It cancels executing tool calls so the
current turn ends sooner. Delivery still happens where it always does, on the
next `call_llm`. Interrupt's only job is "stop the work in front of the queue."

That also fixes what I got wrong: interrupt on a run with no executing tools is a
no-op, and correctly so. If the run is not looping, that is a RESUME problem —
see specs/pause-and-resume.md. Queueing does not solve it and must not try.

## Rules this design is held to

- One writer, one deliverer, one continuation rule. If a second delivery path
  appears, that is the bug.
- Queueing never inspects pause state. Resume never inspects the mailbox.
- No parallel record of anything Temporal already owns.
- No back-compat: superseded code is deleted, not deprecated.
