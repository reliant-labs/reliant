# 2026-10-10: a waking machine was hidden behind a wedged run

## What the user saw

Chat `66a045ce` (prod, ~14:42 UTC): two "continue" messages, then
"Processing •••". Nothing said the machine was waking, or that the message
would send when it connected — the indicator #672 and #680 had shipped.

At that moment the user's machine (`ws-ws-2aab1465`) was suspended and being
resumed: CR phase Pending, "Starting a machine for your workspace. A first
start usually takes a few minutes.", pod Pending on a 17-second-old node.

## Why the indicator did not show

Every machine surface keyed on the RUN, not on the machine:

- The transcript footer (`ChatPresenter`) said "Queued — will send when your
  machine connects" only once the chat's activity was `WAITING_FOR_DAEMON`.
  Otherwise it was `ChatThinkingIndicator`, whose cycling copy includes
  "Processing".
- The composer line (`ComposerWakeStatus`) said "Waking …" only while a send
  was in flight or the run was in that same wait.

The run never reached the wait. The user's "continue" resumed it
(`[PauseService] Resuming workflow`, activity RUNNING) onto a build that could
not replay it: every workflow task failed `[TMPRL1100] nondeterministic
workflow` (attempt 1 at 14:42:19, 2 at 14:42:44, 3 at 14:43:44, 4 at
14:44:44). The run was started 16 hours earlier on #641's build, the shape
#672 predicted it could not recover.

## Why the reconciler did nothing for three minutes

The wedge path needed all of:

1. the pending task's attempt count at 5 or more before it even read the
   history (`DefaultWedgeAttemptThreshold`) — attempt 5 was due ~14:45:44;
2. 3 consecutive passes spanning 3 minutes after that (`observeTask`);
3. the attempt count to move again while watched.

That is ten minutes or more for a fresh wedge. An operator terminated the run
at 14:45:28. The reconciler then repaired the row (`Status mismatch detected`,
`Workflow ended terminally without reporting it`) and told the user to send a
message — that path never called #680's continuation, so the user's
"continue" stayed unanswered.

## Fix

- **The machine is read from the machine.** `lib/chatMachineNotice` resolves
  the chat's machine (pinned, else `defaultMachineDaemon`) from the registry.
  Whenever work waits on it (a live run, a run held for or a message queued
  for the machine, a send in flight) and it is starting, reconnecting, asleep
  or failed, the footer says so, whatever the run is doing: "Waking your
  machine — your message will send when it connects · 42s", "Your machine
  failed to start" with Try again, "Your machine is asleep" with Start it. The
  composer keeps only "Continue without machine" so there is one statement of
  the machine's state.
- **A silence is named.** The thinking indicator says "Still working — no
  response yet" after 60s with no output (new message, streamed text, or new
  activity), with Run status and Stop.
- **A wedge is confirmed by evidence, not by a clock.** The reconciler reads
  the history from the first retry (attempt 2) and ends the run once a worker
  polling now has failed it again — the attempt count moved, or a newer
  `WorkflowTaskFailed` was recorded (a signal restarts the count at 1). No
  wall-clock window: a timing-out task never qualifies (the 2026-09-29 false
  wedge), and the "failed again while watched" rule still protects a run the
  deploy that fixes it would heal. On the 66a045ce timeline this ends the run
  at the 14:43:58 pass, 1m39s after the first failure.
- **Any end the run did not report carries on.** The silent-termination
  repair (an operator's terminate, the history cap, an unreported failure)
  now calls the same `ContinueQueued` as wedge recovery, for a run the row
  still had running. Exactly once: `ContinueQueued` claims under the chat's
  run-control lock and starts nothing unless the root is FAILED and closed;
  the reconciler acts only on the pass whose CAS wins. The continuation gets
  its own 90s context (it shared the pass's 30s, and reading a closed run's
  inputs replays its whole history), and a chat is continued automatically at
  most twice per 30 minutes. The chat reads "Recovered from an internal error;
  continuing from your last message."

A paused run that ends silently is reported but not continued: it was waiting
for the user, and starting it would override a pause.
