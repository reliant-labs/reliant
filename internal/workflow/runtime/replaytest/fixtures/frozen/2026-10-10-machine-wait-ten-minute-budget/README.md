# Frozen: the 10-minute machine wait (recorded before the durable wait)

`machine_wait.json`, recorded on `origin/main` at `43c3e8fc` by
`TestGenerateFixture_MachineWait`: a chat's machine is still starting when its
run begins, the first `PreflightDaemonCheck` polls a whole 60s slice and
reports `waiting`, the run sleeps the old 2s backoff timer, and the second
slice finds the machine up. That code gave up after 10 minutes of slices.

The change that replaced it (a timer that backs off from 30s to 5m between
short checks, woken by the `machine_wait` signal, bounded by the machine's own
states and a 6-hour cap) needs **no version gate**, and this set is the
evidence. A slice-and-timer is still an activity then a timer: Temporal
matches a replayed `StartTimer` by its timer id, not its duration, and an
activity by its type, not its input. So a run recorded by the old loop replays
through the new one command for command — and, after the deploy, simply keeps
waiting longer instead of failing at 10 minutes. Verified by replaying this
history against the new code; a change that schedules something else between
the first slice and the next check breaks it.

Retire this set once no run recorded before the durable wait can still be open
(a waiting run is bounded by the old 10-minute budget, so in practice: any time
after that deploy has been out for a day).
