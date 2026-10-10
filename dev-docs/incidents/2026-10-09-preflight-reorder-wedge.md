# 2026-10-09: the preflight reorder wedged every in-flight run

## What users saw

- Chat `97654413` read "active" for hours with nothing running. The user's
  "continue" (22:22:59 UTC) was saved and never answered.
- "I sent a message while my machine was spinning up and it just looks
  stuck" — nothing on screen said the message was waiting for the machine.
- The user's only cloud machine was crash-looping at the same time, which
  made the stuck chat look like a machine problem. It was not: the run never
  got far enough to ask for the machine.

## Root cause: an ungated command-sequence change

#641 (`fbca55fe`) moved `PreflightDaemonCheck` from before the "started"
`WorkflowStatus` notification to after it, without a `workflow.GetVersion`
gate, and regenerated every replay fixture. The replay suite passed, because a
regenerated fixture only proves the code replays what the same code recorded.

The 21:30 UTC worker rollout carried it. A run recorded before it schedules
`PreflightDaemonCheck` as activity 11; the new code schedules `WorkflowStatus`
there. The next workflow task of every such run failed:

```
[TMPRL1100] nondeterministic workflow: history event is ActivityTaskScheduled:
(ActivityId:11, ActivityType:(Name:PreflightDaemonCheck) …
replay command is ScheduleActivityTask: (ActivityId:11, ActivityType:(Name:WorkflowStatus) …
```

Temporal retries a failed workflow task forever, backing off to ~10 minutes
between attempts: `97654413` reached attempt 33, `3f03dc31` (an archived chat
whose archive-cancel triggered the replay) 32. A third prod run, `098c210d`,
had the same shape and would have wedged on its next message.

## Why it stayed "active"

- The send that triggered the replay marked the root workflow ACTIVE. Its six
  sub-agent rows were ACTIVE too, and `chats_with_activity` reports RUNNING
  for any ACTIVE workflow row in the chat.
- The reconciler saw the wedge and did nothing: its wedge path had been
  switched off after the 2026-09-29 false wedge, because the attempt counter
  it read cannot tell a timing-out task from a failing one. It logged
  "Intervention disabled: would have terminated wedged workflow 97654413".
- The fixtures README claimed such runs "are recovered by the reconciler".

## Why the enqueue looked stuck

Independent of the wedge: a run held for its machine has activity
`WAITING_FOR_DAEMON`, which is not RUNNING. The web gated the transcript
footer on "running", and its "waiting for machine" check read the chat query,
a snapshot that does not follow the run into the wait. So a held run showed
nothing at all under the user's message, and the queued strip said the
message was waiting for "the agent's next turn".

## Fixes

- `preflight-after-started` version gate: a marker-less history runs the
  check before "started", as recorded. Verified by replaying the real prod
  histories of `97654413`, `3f03dc31` and `098c210d` (with their claim-checked
  payloads) against the gated code: all three replay cleanly.
- Frozen fixture sets (`replaytest/fixtures/frozen/`), pinned by checksum and
  replayed alongside the current set; `make freeze-replay-fixtures`; the
  README now requires a gate plus a frozen set for a command-sequence change.
- The reconciler acts on a wedge again, by default, but only when the run's
  history records a workflow-task FAILURE every retry repeats, and only after
  a worker has retried it and failed again while the reconciler watched (so
  the deploy that fixes a break cannot race it). It ends the sub-agents with
  the run, and a run with a pending cancel ends cancelled and silent.
- The resume path never reset-and-replays a history whose last workflow task
  failed TMPRL1100 (`ErrReplayDiverged`); it starts a fresh run at the
  checkpoint.
- Web: the held-run state comes from the activity store; the footer says
  "Queued — will send when your machine connects" under an unanswered message
  (or "Waiting for your machine" mid-turn); the queued strip says what its
  entries wait on; a machine that FAILED to start is named with its reason,
  Try again, Manage machines and Continue without machine.

## Not recoverable by the gate

Runs recorded by #641's own build (21:30 UTC to the gate's deploy) are
marker-less too, and record the new order. No `GetVersion` call — and nothing
a workflow can read deterministically before the check — separates them from
the old shape, so they wedge once on their next workflow task after the gate
deploys. In prod that is one paused chat (`66a045ce`). The reconciler ends it
after a confirmed retry, and the user's next message continues it from its
checkpoint in a fresh run.
