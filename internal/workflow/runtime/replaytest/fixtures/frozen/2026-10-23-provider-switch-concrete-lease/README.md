# Frozen: provider switching leased the root concrete model to usage-waiting spawns

Recorded on main `9f51649e`, before provider-switch-by-tag changes. The generator
starts builtin://agent with a general spawn; the spawned call is refused with a
four-hour usage window. Once its durable marker is visible, a root picker
`update_workflow_state` selects `gpt-6.1-sol@codex` and the existing capacity wake
carries `UseModel: gpt-6.1-sol@codex`.

The old workflow skips the descendant's preset-owned model in the global update,
then answers the usage wait with a concrete-model fallback lease. It schedules a
`UsageLimitWait` notice (`notice: switch_provider`) before retrying the call.
This loses the descendant's semantic selector and is precisely the wrong path
for a tag-preserving provider switch.

`chat-provider-switch-preserve-tags/update/<n>` versions each observed model
update. Histories without its marker retain the old concrete-lease command
sequence. New live updates strictly bind routing to the selected driver while
leaving descendant tag selectors intact and clearing the obsolete lease.

Verified by replacing the update gate with `if false`: replay failed with
`[TMPRL1100] lookup failed for scheduledEventID to activityID:
scheduleEventID: 140, activityID: 140`. Restoring the gate replays successfully.

Do not regenerate this file. SHA256SUMS pins the original bytes.
