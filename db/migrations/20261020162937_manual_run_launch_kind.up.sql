-- forge:backward-compatible — the check only widens: every kind the previous release writes is still allowed, and it reads a manual.run row as an unknown launch kind, which the UI renders generically.

-- manual.run is a run a person started by pressing "Run now" on a workflow. It is a
-- new trigger_events kind, so the check admits it.
ALTER TABLE trigger_events DROP CONSTRAINT trigger_events_kind_check;
ALTER TABLE trigger_events ADD CONSTRAINT trigger_events_kind_check CHECK (kind = ANY (ARRAY[
    'chat.start'::text, 'schedule'::text, 'agent.start_run'::text, 'builder.test'::text,
    'webhook'::text, 'integration'::text, 'workflow_event'::text, 'manual.run'::text
]));
