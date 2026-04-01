-- +goose Up
CREATE TABLE IF NOT EXISTS background_process_output (
    id BIGSERIAL PRIMARY KEY,
    process_id TEXT NOT NULL REFERENCES background_processes(id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    stream TEXT NOT NULL,
    line TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_bg_proc_output_process_seq 
    ON background_process_output(process_id, seq);

-- +goose Down
DROP INDEX IF EXISTS idx_bg_proc_output_process_seq;
DROP TABLE IF EXISTS background_process_output;
