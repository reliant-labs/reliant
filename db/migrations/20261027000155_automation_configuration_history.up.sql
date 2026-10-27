-- forge:backward-compatible — adds configuration history without changing existing writes
CREATE TABLE trigger_changes (
    id text PRIMARY KEY,
    trigger_id text NOT NULL REFERENCES triggers(id) ON DELETE CASCADE,
    user_id text NOT NULL,
    chat_id text NOT NULL DEFAULT '',
    run_id text NOT NULL DEFAULT '',
    tool_call_id text NOT NULL DEFAULT '',
    operation text NOT NULL,
    before_state jsonb,
    after_state jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX trigger_changes_history ON trigger_changes(user_id, trigger_id, created_at DESC, id DESC);
COMMENT ON TABLE trigger_changes IS 'forge:no-entity';
