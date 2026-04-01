-- +goose Up
-- Background process output lines for distributed mode.
-- Output is written by the daemon's PersistenceCallback and read by the api-server's DBBackgroundProcessProvider.
CREATE TABLE IF NOT EXISTS background_process_output (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    process_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    stream TEXT NOT NULL,   -- 'stdout' or 'stderr'
    line TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (process_id) REFERENCES background_processes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_bg_proc_output_process_seq 
    ON background_process_output(process_id, seq);

-- +goose Down
DROP INDEX IF EXISTS idx_bg_proc_output_process_seq;
DROP TABLE IF EXISTS background_process_output;
