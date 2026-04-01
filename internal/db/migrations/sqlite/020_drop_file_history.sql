-- +goose Up
DROP TABLE IF EXISTS file_history;

-- +goose Down
-- Recreate file_history table if needed to rollback
CREATE TABLE file_history (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    thread TEXT NOT NULL,
    path TEXT NOT NULL,
    content TEXT NOT NULL,
    version TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE,
    UNIQUE(chat_id, thread, path, version)
);

CREATE INDEX idx_file_history_chat ON file_history(chat_id);
CREATE INDEX idx_file_history_thread ON file_history(chat_id, thread);
CREATE INDEX idx_file_history_path ON file_history(path);
CREATE INDEX idx_file_history_chat_path ON file_history(chat_id, path, created_at DESC);
